# EzTest 0.2 P1 控制面 + 澄清 — 实施规格

> 状态：用户已审核批准（2026-10-31）。执行口径：全部按推荐；附件支持现代 docx/pptx + 旧 .doc/.ppt 明确失败；`Event.seq`/`snapshot_cursor` 修代码为整数；版本 running 由首条对话消息/首个需求/首次确认触发；团队频道补只读 API + 面板；Markdown 用 `react-markdown`；真实 LLM 使用 `.env` 的小写 `llm_*` 配置（`llm_model_name=deepseek-flash`，仅验收脚本读取，API 仍严格 4 项 env）；上下文窗口从网关模型目录读取，缺失回退设置。
> 阶段：P1（控制面 + 澄清）。阶段文件：`docs/version-V0.2/phases/phase-01-runtime-clarification.md`。
> 前置 handoff：`docs/version-V0.2/phases/phase-00-handoff.md`（P0 completed）。
> 计划：`docs/superpowers/plans/2026-10-31-eztest-v02-p1-runtime-clarification.md`。
> 本规格只实现 P1；P2+ 内容只进入 handoff/backlog/契约保留位，不实现、不提前建表。

## 0. 依据、阶段映射与澄清

**权威顺序**：`architecture-design.md`、`high-level-design.md`、`research/decision-log.md`、`research/open-decisions.md`、`research/capability-matrix.md`、`phases/phase-01-runtime-clarification.md`、`contracts/*`（P0 frozen）、P0 handoff。

**阶段映射冲突（已按权威阶段表裁决）**：会话启动提示词的读取清单误用了 P0 模板（`phase-00-platform-foundation.md` + “无前置 handoff”），但阶段复制表明确规定 `P1 → phase-01-runtime-clarification.md`、`PREV_HANDOFF=phase-00-handoff.md`；仓库中 P0 已完成并冻结。本会话按 **P1 = phase-01** 实施，不重做 P0，并在 handoff 记录该提示词替换错误。

**P0 基线证据**：`powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` → `EXIT=0`；Go 单元+集成全部 `ok`；前端 30 文件 / 243 tests passed；日志 `.tmp/p1/baseline-test.log`。

**关键设计事实（带来源）**
- 任务板是唯一执行队列；`work_item` 状态 `open/claimed/running/waiting_human/done/failed/dead`；领取用 `FOR UPDATE SKIP LOCKED`；租约 + `lease_epoch` fencing；reconciler 每 2–5s 扫描期望状态（architecture §5.3，HLD §3）。
- Agent 协作 = 共享任务板 + 版本×团队频道 + 黑板产物；无中心编排 Agent；Agent 不直接调用 Agent（D35–D37）。
- 所有 Agent 工作先变成 `work_item`；聊天消息是 `chat.turn` 任务（architecture §5.2、HLD §3.1）。
- run scope 由运行时注入；工具 schema **不接受**模型传 `versionId`（architecture §7.2–7.3，HLD §4.1–4.3）。
- 检索/上下文：版本包、任务包、会话记忆（最近窗口+滚动摘要+钉住事实）、团队频道摘要、工具/证据；按 token 预算；`context_overflow` 不判失败（architecture §9，D41–D44，HLD §5）。
- 需求 = 逻辑对象 + 不可变版本，状态 `draft/clarified/closed/obsolete`；编辑/回退生成新 draft；确认冻结版本；AI 不能替用户确认或删除（D14–D18）。
- 澄清页主 UI 是 AI 对话，辅 UI 是需求列表/编辑器；用户单条/批量确认；AI 可提议回退（D16–D18，HLD §6.3/§8）。
- 附件支持 doc/pdf/md/ppt，解析为文本/图片引用（phase-01；D48）。
- 团队频道摘要：每 20 条或 10 分钟生成，保留最近 N 版；帖子全量审计；不全文回放（OD-9，settings `team_room.*`）。
- 版本状态只是投影：`initial → running` 由首个对话消息/首个需求/首次确认触发；P1 不写 `completed`（HLD §2.4/§6.7）。
- P1 新配置只能进 DB 设置中心；`.env` 严格 4 项（D49/D50）。

## 1. 目标与退出标准

**目标**：建立可恢复的任务板/租约/reconciler、Agent runtime/上下文、团队频道；打通“新增对话 → AI 澄清 → 需求版本 → 用户确认/回退”的垂直闭环。

**退出标准（逐条可验证）**
1. 用户新建澄清对话 → Host 读取版本上下文/附件 → 产出 draft 需求；需求可在 UI 编辑、单条/批量确认。
2. AI 可提议回退已澄清需求；用户确认后生成新 draft，版本递增；P2 stale 钩子被调用且事件携带 `affected_case_version_ids`（P1 为空）。
3. 任务板并发领取不重复；租约过期可恢复；旧 epoch 写回被拒绝；重复扫描不产生重复任务。
4. 上下文压缩后钉住事实/最近窗口/任务约束不丢；`context_overflow` 场景 run 仍成功（或明确降级），不进入失败终态。
5. 附件 md/txt/docx/pptx/pdf 可解析为文本引用；损坏/不支持格式有明确失败状态，不阻塞消息。
6. 版本详情 `requirements` Tab 可用；`initial→running` 投影、SSE 实时更新、设计待办 CTA 占位可见。
7. `scripts/test.ps1` 全绿；P1 E2E/验收脚本通过；契约冻结；handoff 完成。

**不在 P1**
- 设计/脑图/场景/用例/design_scope/design.round（P2）；执行/波次/意图（P3）；交付/问题/报告/链路（P4+）；代码智能/视觉/质量/搜索/待办/Agent 中心（P5/P6）；P9。
- 0.1 数据迁移；多副本部署编排（P7）；0.2 全量 E2E/性能/SLO（P8）。真实 LLM 验收在 P1 必跑：使用 `.env` 的小写 `llm_api_key/llm_base_url/llm_model_name`（`deepseek-flash`），由验收脚本经 admin API 写入 `llm_config` 并刷新模型目录；API 进程仍只读严格 4 项 EzTest env。
- 通用 `Idempotency-Key` 中间件（P1 的确认/消息用自然幂等 + 唯一约束；handoff 记录）。

## 2. 实施策略与仓库边界

- 后端继续保持模块化单体、四层目录、`cmd/eztest` 唯一组装根；跨上下文只依赖 application 接口。
- **扩展 `version` 上下文**（`backend/internal/version/`）：需求/需求版本/确认/关系/对话/消息/摘要/附件。理由：HLD 将版本、需求、澄清、对话归为同一垂直域；P0 已有 version 上下文。
- **新增 `agent` 上下文**（`backend/internal/agent/`）：work_item/attempt、团队频道、agent profile/run/message/context packet、context builder、工具注册/执行、LLM run loop、reconciler、worker。`Agent.md §6.1` 已预留该上下文。
- 运行时后台 worker/reconciler 由 `cmd/eztest` 装配并随进程启停；单实例假设写在包注释与 handoff。
- 前端新增 `frontend/src/features/clarification/`，路由仍由 `src/app/(app)/versions/[id]` 组装；HTTP/SSE 必须走 `src/lib/api-client.ts`/`sse-client.ts`。
- 不修改 `docs/version-V0.2/**` 设计文档；只更新 `contracts/`、本 spec/plan、迁移、测试证据、`phases/phase-01-handoff.md`。

## 3. 数据模型与迁移（只追加）

新增 7 个迁移，追加到 `order.txt`；不改 P0 已应用迁移。所有新业务主键为雪花 `BIGINT`；JSON 中 ID 为十进制字符串；新表审计列 `created_at/updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`（HLD 明确沿用）。跨上下文只做逻辑引用，不建 FK。

### 3.1 `agent_0001_work_item.sql`
`work_item`：
`id BIGINT PK`、`version_id BIGINT NOT NULL`、`team TEXT NOT NULL`、`type TEXT NOT NULL`、`status TEXT NOT NULL DEFAULT 'open'`（CHECK 7 态：open/claimed/running/waiting_human/done/failed/dead；P1 只写 open/claimed/running/done/failed/dead，不写 waiting_human）、`priority INT NOT NULL DEFAULT 0`、`dedupe_key TEXT NOT NULL`、`depends_on BIGINT NULL`、`payload JSONB NOT NULL DEFAULT '{}'`、`claimed_by TEXT NULL`、`lease_until TIMESTAMPTZ NULL`、`lease_epoch BIGINT NOT NULL DEFAULT 0`、`attempts INT NOT NULL DEFAULT 0`、`max_attempts INT NOT NULL DEFAULT 3`、`last_error TEXT NULL`、`available_at TIMESTAMPTZ NOT NULL DEFAULT now()`、`created_at/updated_at`。
约束/索引：`uk_work_item_dedupe UNIQUE(version_id, type, dedupe_key)`；`idx_work_item_claim (status, available_at, priority DESC, id)`；`idx_work_item_lease (status, lease_until)`；`idx_work_item_version (version_id, status, id)`。

`work_item_attempt`：`id BIGINT PK`、`work_item_id BIGINT NOT NULL REFERENCES work_item(id)`、`attempt INT NOT NULL`、`status TEXT NOT NULL`、`error TEXT NULL`、`started_at`、`finished_at`；`UNIQUE(work_item_id, attempt)`；索引 `idx_work_item_attempt_item (work_item_id, id)`。

### 3.2 `agent_0002_team_room.sql`
`team_room_post`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`team TEXT NOT NULL`、`kind TEXT NOT NULL`（CHECK `finding/decision/blocker/handoff/status`）、`content TEXT NOT NULL`、`refs JSONB NOT NULL DEFAULT '[]'`、`agent_run_id BIGINT NULL`、`created_by BIGINT NULL`、`created_at`；索引 `(version_id, team, id DESC)`、`(version_id, created_at)`。
`team_room_digest`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`team TEXT NOT NULL`、`summary TEXT NOT NULL`、`pinned_post_ids JSONB NOT NULL DEFAULT '[]'`、`token_estimate INT NOT NULL DEFAULT 0`、`version INT NOT NULL`、`updated_at`；`UNIQUE(version_id, team, version)`；索引 `(version_id, team, version DESC)`。

### 3.3 `agent_0003_runtime.sql`
`agent_profile`：`id BIGINT PK`、`key TEXT NOT NULL UNIQUE`、`name TEXT NOT NULL`、`system_prompt TEXT NOT NULL`、`allowed_tools JSONB NOT NULL DEFAULT '[]'`、`model TEXT NULL`、`budgets JSONB NOT NULL DEFAULT '{}'`、`enabled BOOL NOT NULL DEFAULT true`、`created_at/updated_at`。
`agent_run`：`id BIGINT PK`、`profile_key TEXT NOT NULL`、`work_item_id BIGINT NULL`、`conversation_id BIGINT NULL`、`status TEXT NOT NULL DEFAULT 'running'`（CHECK `running/done/failed/aborted/interrupted`）、`terminal_reason TEXT NULL`、`turns INT NOT NULL DEFAULT 0`、`usage JSONB NOT NULL DEFAULT '{}'`、`owner_instance TEXT NOT NULL`、`lease_epoch BIGINT NOT NULL DEFAULT 0`、`context_packet_id BIGINT NULL`、`started_at`、`finished_at`；索引 `(work_item_id)`、`(conversation_id, id DESC)`、`(status)`。
`agent_message`：`id BIGINT PK`、`run_id BIGINT NULL`、`conversation_id BIGINT NULL`、`role TEXT NOT NULL`（CHECK `system/user/assistant/tool`）、`parts JSONB NOT NULL DEFAULT '[]'`、`tool_calls JSONB NOT NULL DEFAULT '[]'`、`tool_call_id TEXT NULL`、`usage JSONB NOT NULL DEFAULT '{}'`、`created_at`；索引 `(run_id, id)`、`(conversation_id, id)`。
`context_packet`：`id BIGINT PK`、`run_id BIGINT NOT NULL`、`version_id BIGINT NOT NULL`、`sections JSONB NOT NULL`、`token_counts JSONB NOT NULL`、`hash TEXT NOT NULL`、`created_at`；索引 `(run_id)`。

### 3.4 `version_0002_requirement.sql`
`requirement`：`id BIGINT PK`、`version_id BIGINT NOT NULL REFERENCES version(id)`、`current_version_id BIGINT NULL`、`status TEXT NOT NULL DEFAULT 'draft'`（CHECK `draft/clarified/closed/obsolete`）、`target_version_sut_ids JSONB NOT NULL DEFAULT '[]'`、`closed_at TIMESTAMPTZ NULL`、`closed_by_run_id BIGINT NULL`、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`source_conversation_id BIGINT NULL`、`created_at/updated_at`；索引 `(version_id, status, id DESC)`。
`requirement_version`：`id BIGINT PK`、`requirement_id BIGINT NOT NULL REFERENCES requirement(id)`、`revision INT NOT NULL`、`title TEXT NOT NULL DEFAULT ''`、`description TEXT NOT NULL DEFAULT ''`、`acceptance_criteria TEXT NOT NULL DEFAULT ''`、`priority TEXT NOT NULL DEFAULT 'P2'`（CHECK `P0/P1/P2/P3`）、`constraints JSONB NOT NULL DEFAULT '{}'`、`target_version_suts JSONB NOT NULL DEFAULT '[]'`、`target_pages JSONB NOT NULL DEFAULT '[]'`、`target_apis JSONB NOT NULL DEFAULT '[]'`、`content_hash TEXT NOT NULL`、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`created_at`；`UNIQUE(requirement_id, revision)`；索引 `(requirement_id, id DESC)`。
`requirement_confirmation`：`id BIGINT PK`、`requirement_version_id BIGINT NOT NULL REFERENCES requirement_version(id)`、`scope TEXT NOT NULL`（CHECK `requirement/batch`）、`batch_id TEXT NULL`、`actor_id BIGINT NOT NULL`、`confirmed_at`、`affected_requirement_ids JSONB NOT NULL DEFAULT '[]'`；`UNIQUE(requirement_version_id)`；索引 `(batch_id)`、`(actor_id, confirmed_at DESC)`。
`requirement_relation`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`from_requirement_id BIGINT NOT NULL`、`to_requirement_id BIGINT NOT NULL`、`kind TEXT NOT NULL`（CHECK `depends_on/conflicts_with/overlaps/shares_scope`）、`description TEXT NOT NULL DEFAULT ''`、`source_revision_id BIGINT NULL`、`created_by BIGINT NULL`、`created_by_run_id BIGINT NULL`、`created_at`；`UNIQUE(version_id, from_requirement_id, to_requirement_id, kind)`；CHECK `from_requirement_id <> to_requirement_id`。

### 3.5 `version_0003_conversation.sql`
`conversation`：`id BIGINT PK`、`version_id BIGINT NOT NULL REFERENCES version(id)`、`kind TEXT NOT NULL`（CHECK `clarification/design`；P1 API 只创建 clarification）、`title TEXT NOT NULL`、`status TEXT NOT NULL DEFAULT 'active'`（CHECK `active/closed`）、`created_by BIGINT NULL`、`created_at/updated_at`；索引 `(version_id, kind, id DESC)`。
`conversation_message`：`id BIGINT PK`、`conversation_id BIGINT NOT NULL REFERENCES conversation(id)`、`role TEXT NOT NULL`（CHECK `user/assistant`）、`parts JSONB NOT NULL DEFAULT '[]'`、`refs JSONB NOT NULL DEFAULT '[]'`、`token_usage JSONB NOT NULL DEFAULT '{}'`、`actor_id BIGINT NULL`、`agent_run_id BIGINT NULL`、`client_message_id TEXT NULL`、`reply_to_message_id BIGINT NULL`、`created_at`；`UNIQUE(conversation_id, client_message_id)`；索引 `(conversation_id, id)`、`(conversation_id, reply_to_message_id)`。
`conversation_summary`：`id BIGINT PK`、`conversation_id BIGINT NOT NULL REFERENCES conversation(id)`、`summary TEXT NOT NULL`、`pinned_facts JSONB NOT NULL DEFAULT '[]'`、`token_estimate INT NOT NULL DEFAULT 0`、`version INT NOT NULL`、`updated_at`；`UNIQUE(conversation_id, version)`。

### 3.6 `version_0004_requirement_attachment.sql`
`requirement_attachment`：`id BIGINT PK`、`version_id BIGINT NOT NULL`、`requirement_id BIGINT NULL`、`requirement_version_id BIGINT NULL`、`conversation_message_id BIGINT NULL`、`content_ref_id BIGINT NOT NULL`、`name TEXT NOT NULL`、`mime TEXT NOT NULL`、`size BIGINT NOT NULL`、`extraction_status TEXT NOT NULL DEFAULT 'pending'`（CHECK `pending/processing/done/failed/skipped`）、`extracted_content_ref_id BIGINT NULL`、`extraction_error TEXT NULL`、`created_by BIGINT NULL`、`created_at/updated_at`；索引 `(version_id, requirement_id)`、`(conversation_message_id)`、`(extraction_status, id)`。
说明：`extracted_content_ref_id/extraction_status/extraction_error/requirement_version_id` 是 HLD“关键字段”之外的运行态字段，P1-D5 记录；**不把附件全文复制进需求版本**（OD-10：附件作为来源引用）。

### 3.7 `eventing_0002_event_index.sql`
追加 `idx_event_channel_created_at ON event(channel, created_at)`，收口 P0 handoff §2 记录的 event index 漂移；不修改 P0 迁移与既有索引。

## 4. 控制面：work_item、租约、fencing、reconciler、worker

### 4.1 任务服务
`agent/application` 定义 `WorkQueue`：
- `Ensure(ctx, NewWorkItem) (WorkItem, bool created, error)`：按 `(version_id,type,dedupe_key)` 幂等插入；重复时返回既有项且 `created=false`。
- `Claim(ctx, claimant string, lease time.Duration) (WorkItem, bool, error)`：`SELECT ... FOR UPDATE SKIP LOCKED`，只取 `status='open' AND available_at<=now()`，按 `priority DESC, id ASC`；置 `claimed`、`lease_epoch+1`、`attempts+1`、`claimed_by`、`lease_until`；同事务写 `work_item_attempt`。
- `MarkRunning(ctx,id,epoch)`、`Renew(ctx,id,epoch,lease)`、`Complete(ctx,id,epoch)`、`Fail(ctx,id,epoch,err,retryable)`。
- `Fail` 规则：不可重试/达到 `max_attempts` → `dead`；否则 `open`，`available_at=now()+backoff`（`min(2^attempts, 30)s`）。`dead` 保留 `last_error`。
- `RecoverExpired(ctx)`：只把 `status IN ('claimed','running') AND lease_until<now()` 置 `open`（或达到上限置 `dead`），记录 attempt；启动时调用一次，之后由 worker 周期调用。**不**因进程启动批量中断 running。
- 所有写回带 `WHERE id=$1 AND lease_epoch=$2`；0 行 → `platform.NewError(409,"lease_lost",...)`。

### 4.2 Worker
- `cmd/eztest` 启动 `agent/application.Worker`，随进程 context 停止；单实例。
- 并发上限取 `agent.max_concurrent_runs`；轮询间隔 300ms（常量）；每 10s 续租；租约 30s；`max_attempts` 默认 3。
- 任务类型→profile 映射：`chat.turn→clarification_host`、`requirement.curate→requirement_curator`、`attachment.ingest→context_scout`。
- 领取后：`MarkRunning` → `AgentRunner.Run` → 成功 `Complete`；错误按 `Fail`（模型不可用可重试；参数/作用域错误不可重试）。
- 心跳续租失败 → 放弃写回并记录 `lease_lost`，不覆盖新 owner 的结果。

### 4.3 Reconciler（P1 规则）
每 3s 扫描（常量，落在设计 2–5s）；只做 ensure，不执行业务动作：
1. 版本投影：存在至少一条 `conversation_message` 或存在 requirement 或存在确认，且 `version.status='initial'` → `running`，事件 `version.status_changed`。仅创建空 conversation 不触发。
2. 对话开场：conversation 无任何消息且无 open/claimed/running/done 的 opening 任务 → ensure `chat.turn`（dedupe `opening:{conversation_id}`，payload `{conversation_id, trigger:"opening"}`）。
3. 用户消息回复：存在用户消息且其后（按 id）无 assistant 回复、无 open/claimed/running/done 的对应 `chat.turn` → ensure（dedupe `reply:{message_id}`，payload `{conversation_id,message_id,trigger:"user_message"}`）。
4. 需求抽取：存在用户消息且无 open/claimed/running/done 的 `requirement.curate` → ensure（dedupe `curate:{message_id}`，payload `{conversation_id,message_id}`）。若该消息后已有 Curator 产生的 assistant 消息，则不再 ensure（查询 `conversation_message.agent_run_id` 关联 profile）。
5. 附件：`requirement_attachment.extraction_status='pending'` 且无 active/done ingest 任务 → ensure `attachment.ingest`（dedupe `attachment:{attachment_id}`）。
6. 重复扫描必须幂等；P2+ 的设计/执行规则不实现，只在 handoff 列出。

### 4.4 团队频道与摘要
- `TeamRoomService.Post(ctx, Post)` 写 `team_room_post` 并发布 `team_room.post`；`refs` 是结构化引用数组（`{kind,id}`）。
- 摘要触发：插入后统计“自上次 digest 以来的帖子数 ≥ `team_room.digest_every_n_posts`（20）”或“最近 digest 超过 `digest_interval_minutes`（10）分钟”；生成确定性摘要（按 kind 分组、保留 decision/blocker 为 pinned，截断每条），写新 `team_room_digest.version+1`；保留最近 `digest_keep_versions`（5）版，删除更旧。
- 只读 API：`GET /api/versions/{id}/team-rooms/{team}?limit=`（登录用户；team 枚举 `clarification/design/execution/delivery/steward`）返回 `{data:{team,digest,posts}}`；P1 前端在需求页展示澄清团队摘要/最近帖子（折叠面板），不提供发帖/自由聊天控制面。
- 频道不触发 Agent；人类 `@` 不在 P1 实现（P2+）。

## 5. Agent runtime、上下文与工具

### 5.1 Profile（P1 种子）
启动幂等 seed（`agent_profile.key` 冲突不覆盖管理员修改）：
| key | 名称 | 工具（P1） | 预算 |
|---|---|---|---|
| `clarification_host` | 澄清 Host | `context.version_pack`、`context.task_pack`、`context.team_room_read`、`context.summary_update`、`requirement.read`、`attachment.list`、`attachment.read_text`、`sut.list`、`sut.page_list` | 12 turns / 300s soft / 600s hard |
| `requirement_curator` | 需求 Curator | 上述读工具 + `requirement.draft_create`、`requirement.draft_update`、`requirement.relation_propose`、`requirement.reopen_propose` | 8 turns / 300s / 600s |
| `context_scout` | 上下文 Scout | `attachment.list`、`attachment.read_text`、`attachment.image_ref`、`sut.list`、`sut.page_list`、`context.team_room_read` | 4 turns / 120s / 300s |

system_prompt 常量定义角色边界：Host 只对话不确认；Curator 只产 draft/关系/回退提议，不确认不删除；Scout 只读与总结，不改需求。Prompt 必须包含 `[profile:<key>]` 标记供 fake LLM 识别；真实模型不受影响。

### 5.2 LLM 网关
- 在 `llm/application` 增加 `ChatGateway` 端口与 `ChatRequest/ChatResponse`：OpenAI-compatible `POST {base_url}/chat/completions`，支持 `tools`、`tool_choice:"auto"`、`temperature=0`（P1 确定性优先）、`max_tokens`（模型 max_output 或 4096）。
- 目标解析：默认 `llm_config.is_default=true`；模型优先 profile.model（匹配 `llm_model.model`），否则该配置第一条模型；无配置/模型 → `ai_unavailable`。
- 超时 `llm.request_timeout_seconds`；重试 `llm.max_retries`（仅网络/5xx/429；4xx 不重试）；错误文本不得包含 API key。
- 预算口径：**优先使用 LLM 网关注册的模型元数据** `llm_model.context_window / max_output`（由 `/models` refresh 写入，`ResolveModel` 解析）；设置 `context.window_tokens / output_reserve_tokens` 仅在元数据缺失/为 0 时回退。预算 = 模型窗口 − 输出预留（不超过模型 max_output，至少保留 2048 工具预留）；`context.compact_threshold` 仍用设置。逐模型窗口不再推迟到 P8（用户裁决，Q23 更新）。
- `llm_model` 元数据缺失（如真实上游 `/models` 不返回窗口）时按设置回退并在 handoff 记录，不伪造模型能力。
- 真实上游与 fake 上游都走同一网关；fake LLM 扩展见 §5.6。

### 5.3 Context packet
每 run 重建并持久化 `context_packet`（sections + token_counts + hash）；分区：
1. `system`：profile prompt + 任务目标/约束/验收 + 注入 scope（不参与压缩）。
2. `version_pack`：版本摘要、`version_sut`/SUT 名称、页面/API 计数与关键名称、需求列表摘要（id/revision/title/status/priority）、已澄清/草案计数、附件元数据、最近对话摘要。（可降级）
3. `conversation`：滚动摘要 + pinned facts + 最近 `context.keep_recent_messages` 条消息（全文；超出预算时按预算截断）。
4. `team_room`：最新 digest + 与本任务相关帖子（按 refs/team 过滤，最多 10 条）。
5. `artifacts`：需求/附件/团队帖引用（id/kind）。
Token 估算：`runes/2 + 16` 每段（中文保守），总预算 = `ModelBudget.ContextWindow`（缺失时 `context.window_tokens`）- 输出预留（不超过 `ModelBudget.MaxOutput`，缺失时 `context.output_reserve_tokens`）- tool reserve(2048)，且不低于最小可用预算。
- 压缩触发：估算 > `context.compact_threshold`（0.7）* 可用预算。顺序：保留 pinned/最近窗口/任务约束 → 旧消息压成摘要（优先 LLM 摘要，失败回退确定性摘要）→ 仍超则裁剪 version_pack/team_room 可选段 → 仍超则只留 system + task + 最近 4 条。写 `conversation_summary`（version+1）与 `context.compacted` 事件；**不判失败**。
- LLM 返回上下文超限（`context_length_exceeded`/400 含 context）→ 预算减半、强制压缩后重试一次；再失败 → 最小上下文重试；仍失败才按 `ai_unavailable` 失败（记录 terminal_reason=`context_overflow_unrecoverable`，handoff 记录）。
- 工具结果 > `agent.tool_result_max_bytes`（32KiB）→ 上传为 text content_object，返回 `{head, sha256, content_ref, truncated:true}`；agent_message 记录引用。

### 5.4 Run loop
1. 加载 profile、work item payload、conversation（如有）、scope。
2. 构建/持久化 context packet；创建 `agent_run`（status running，owner_instance，lease_epoch）。
3. 循环最多 `agent.max_turns`：调用网关（messages + tools）→ 写 `agent_message`（assistant，含 tool_calls/usage）→ 无 tool_calls 则结束；有则逐个执行工具（scope 注入、参数校验、结果截断/落盘），写 tool `agent_message`，继续。
4. Host 的最终 assistant 文本写 `conversation_message`（role assistant，reply_to_message_id）；Curator 的最终消息可包含 `requirement_card` parts；Scout 写团队帖子而非聊天消息。
5. 成功：agent_run `done`；usage/turns 汇总；发布 `agent.run.finished`。失败：`failed`（terminal_reason）；发布 `agent.run.failed`；任务按 `Fail` 重试。
6. 软超时：到达 soft 时限后不再发起新工具调用，要求模型收尾；硬超时取消 context，run `interrupted`，任务可重试。
7. `context_overflow` 不作为失败终态（除非不可恢复，见 5.3）；`context.compacted` 是过程事件。
8. 结果写回全部带 `lease_epoch`/run id；旧 epoch → `lease_lost`，不覆盖。

### 5.5 工具与作用域注入
工具 schema 只含业务参数；runtime 注入并校验 `scope{version_id, conversation_id, requirement_id?, run_id}`。所有工具返回 JSON；业务错误以 `{error:{code,message}}` 返回给模型，不 500。
P1 工具：
- `context.version_pack`、`context.task_pack`、`context.team_room_read`
- `context.summary_update`（Host；只更新 pinned_facts + summary，长度受限，写 conversation_summary 新版本）
- `requirement.read`（列表/单条）
- `requirement.draft_create`（title/description/acceptance_criteria/priority/constraints/target_version_suts/target_pages/target_apis）
- `requirement.draft_update`（requirement_id + 同上；仅 draft；clarified 返回 `state_conflict` 并提示 reopen_propose；生成新 revision）
- `requirement.relation_propose`（from/to/kind/description；校验同版本、非自环）
- `requirement.reopen_propose`（requirement_id/reason；只写一条 conversation_message part `rollback_proposal`，不改状态）
- `attachment.list`、`attachment.read_text`（分页 head+offset）、`attachment.image_ref`（元数据/URL；P1 不做视觉理解）
- `sut.list`、`sut.page_list`（按版本绑定读取）
未列出的 HLD 工具（case/execution/delivery/code 等）P2+ 增量注册；P1 不创建假实现。

### 5.6 Fake LLM（确定性）
扩展 `scripts/fake-llm.cjs`，保持 P0 行为兼容：
- 无 `tools`/无 profile 标记：维持现有 echo 行为（P0 测试不变）。
- 有 `[profile:clarification_host]`：返回中文澄清回复，引用最近用户文本；不调用工具。
- 有 `[profile:requirement_curator]`：第一次调用返回 `requirement.draft_create` tool_call（标题取最近用户文本前 40 字，description/acceptance 固定模板，priority=P1）；收到 tool 结果后返回最终文本并附 `requirement_card`（由 runtime 从工具结果生成，不依赖 fake 文本）。
- 有 `[profile:context_scout]`：返回附件摘要文本（“已读取附件：<name>，共 N 字符”）。
- 支持 `context_length_exceeded` 注入：请求头 `X-Fake-Context-Overflow: 1` 或 system 标记 `[fake:context_overflow]` 首次返回 400；测试验证 runtime 会压缩重试。
- 响应仍确定性（同请求同响应），只使用 loopback。

## 6. 对话、需求与确认域

### 6.1 状态与不变量
- Requirement status：`draft → clarified`（确认）；`clarified → draft`（编辑/回退）；`closed/obsolete` 不在 P1 UI/API 写入（字段保留，P3/P4 使用）。
- 每个 requirement 有不可变 `requirement_version` 序列；`current_version_id` 指向最新；编辑/回退生成 revision+1。
- 确认冻结：确认后该 requirement_version 不再修改；新编辑产生新 draft revision。
- 确认校验：`title` 1–200 字、`description` 1–20000、`acceptance_criteria` 1–20000、`priority ∈ P0..P3`；target 若提供必须属于该版本绑定（version_sut/page/api）。
- AI 不可确认、不可删除、不可直接改 clarified；只能 draft_create/draft_update（draft）、relation_propose、reopen_propose。
- 回退：用户确认后，复制当前版本字段生成新 draft revision，status=draft；事件 `requirement.rolled_back` + `requirement.version_created`；调用 P2 stale 钩子（P1 空实现）并携带 `affected_case_version_ids`。
- 关系：同版本、非自环、kind 合法、唯一；AI 提议落库为候选（无独立确认 UI）。
- 版本投影：首条对话消息（user 或 assistant）/首个 requirement/首次确认将 version `initial → running`；仅创建空 conversation 不触发；P1 不写 completed。

### 6.2 对话与消息
- 创建 conversation（P1 只 clarification）：标题默认 `需求澄清 YYYY-MM-DD HH:mm`，1–120 字；创建 opening 任务。
- 用户消息 parts：`text`（1–20000 字）和/或 `attachment`（≤10 个）；至少一个非空；`client_message_id` 幂等（≤64，唯一）。
- assistant 消息由 runtime 写入；parts 只含 `text`、`requirement_card`、`rollback_proposal`；附件活动通过 `agent.activity`/团队帖子展示，不新增消息 part。
- 历史消息按 id 游标分页（默认 100，最大 200）；SSE 事件包含完整 message 对象。
- 摘要：`conversation_summary` 每 conversation 保留最新版本；`pinned_facts` 最多 20 条、每条 ≤500 字。
- 职责切分（Q3/Q4）：`conversation_message` 只存 UI 线程（user/assistant + parts）；`agent_message` 存 LLM transcript（system/assistant/tool、tool_calls、tool_call_id、usage）；不存在 `agent_session` 表，conversation 即人机会话，run 通过 work_item/agent_run 关联。`conversation_summary` 是线程级记忆，`context_packet` 是单 run 快照，二者都保留。
- 消息 POST 只做“消息落库 + best-effort ensure 任务”；任务由 reconciler 兜底保证（去重键见 §4.3），不依赖跨上下文事务。

## 7. 附件

- 上传复用 P0 `POST /api/content`（multipart，≤10 MiB）；消息 part 引用 `{sha256,name,mime,size}`；服务端校验对象存在/大小/MIME 后创建 `requirement_attachment`（status pending）+ content_ref + ingest 任务。
- 支持格式：`.md/.markdown/.txt`（UTF-8）、`.docx`（OOXML `word/document.xml`）、`.pptx`（`ppt/slides/slide*.xml` 中 `<a:t>`）、`.pdf`（纯文本操作符，`rsc.io/pdf v0.1.1`）；图片 `.png/.jpg/.jpeg/.webp` 只存引用（`image_ref`，不做 OCR/视觉理解）。
- 旧二进制 `.doc/.ppt` 不支持：标记 `failed` + `unsupported_attachment_format`；handoff 记录为 P1 已知缺口（现代格式 docx/pptx 已覆盖）。
- 解析限制：文本提取 ≤2 MiB；PDF ≤50 页；OOXML 解压后 XML ≤20 MiB（防 zip bomb）；路径/文件名只作展示，不写盘；提取文本作为 text content_object + content_ref（owner `requirement_attachment`，role `extracted_text`），不复制全文进需求版本。
- 失败不阻塞消息：附件状态 `failed` + 错误可见；可人工重试（重发消息/后续阶段补 retry API，P1 只记录）。
- `attachment.ingest` 任务：解析 → 更新状态/错误 → 若成功，Scout 生成团队 finding（失败不影响提取状态）；发布 `requirement.attachment_ingested/failed`。

## 8. API 契约（P1 增量；详见 contracts/openapi.yaml）

通用：Bearer 登录；所有 ID/时间/分页/错误信封沿用 P0。P1 全部为登录用户可读写（单团队信任模型，P0-D16）；无 admin 限制。

### 8.1 对话
| 方法/路径 | 说明 |
|---|---|
| `GET /api/versions/{id}/conversations?kind=clarification&limit=&before=` | 列表（id desc），含 id/title/kind/status/message_count/last_message_at/created_at |
| `POST /api/versions/{id}/conversations` | body `{kind:"clarification",title?}`；创建 + opening 任务；201 |
| `GET /api/versions/{id}/conversations/{cid}` | 详情（含 summary? 不含消息） |
| `GET /api/versions/{id}/conversations/{cid}/messages?after=&limit=` | 消息列表（id 升序）；`after` 为数字字符串 |
| `POST /api/versions/{id}/conversations/{cid}/messages` | body `{client_message_id?,parts:[...]}`；写用户消息 + chat.turn + curate 任务（同请求内幂等）；201 返回 message |

### 8.2 需求
| 方法/路径 | 说明 |
|---|---|
| `GET /api/versions/{id}/requirements?status=&limit=&before=` | 列表；含 id/current_version_id/revision/title/status/priority/targets/updated_at/confirmed_at |
| `POST /api/versions/{id}/requirements` | 手工创建 draft（title/description/acceptance_criteria/priority/constraints/targets）；201 |
| `GET /api/versions/{id}/requirements/{rid}` | 详情：当前版本、历史版本摘要、确认记录、关系、附件 |
| `PATCH /api/versions/{id}/requirements/{rid}` | 编辑 → 新 draft revision（clarified → draft）；body 同上 + `reason?`；200 |
| `POST /api/versions/{id}/requirements/{rid}/confirmations` | 单条确认；已确认幂等返回；200 |
| `POST /api/versions/{id}/requirements/confirmations` | 批量 `{requirement_ids:[...]}`；事务内全部校验；已确认项幂等跳过；返回 confirmed/skipped + batch_id |
| `POST /api/versions/{id}/requirements/{rid}/rollback` | 用户确认回退 `{reason?,proposal_message_id?}`；生成新 draft；200 |
| `GET /api/versions/{id}/requirements/{rid}/relations` | 关系列表（也可在详情内） |

`GET /api/versions/{id}` 增量返回 `requirement_counts{total,draft,clarified,closed,obsolete}`、`conversation_count`、`pending_design_count`（=clarified 且无 design_scope 的占位计数；P1 直接=clarified 数）。P1 不新增设计 API。

### 8.3 团队频道（只读）
| 方法/路径 | 说明 |
|---|---|
| `GET /api/versions/{id}/team-rooms/{team}?limit=` | 登录用户；返回 `{data:{team,digest:{summary,pinned_post_ids,token_estimate,version,updated_at}|null,posts:[{id,kind,content,refs,agent_run_id,created_at}]}}`；`limit` 1–100 默认 20，按 id desc；P1 不提供发帖/自由聊天 API |

### 8.4 错误码
`invalid_argument`（400）、`unauthenticated`（401）、`forbidden`（403）、`not_found`（404）、`state_conflict`（409，如确认非法状态/已确认版本再编辑）、`lease_lost`（409）、`ai_unavailable`（503）、`context_overflow`（503，仅不可恢复）、`unsupported_attachment_format`/`extraction_failed`（422，仅作为附件 `extraction_error.code` 返回，消息 POST 本身仍 201）。错误 details 带 `field`/`requirement_id`/`attachment_id` 便于 UI 定位。`platform` 增加上述稳定 reason 常量，HTTP 状态映射沿用 P0 信封。

### 8.5 P1 JSON 字段口径（实现/前端/契约共用）

所有 ID 为十进制字符串；时间 RFC3339；成功 `{data:...}`；列表 `{data:[],next_cursor:"",has_more:false}`。

```json
ConversationSummary = {id,title,kind,status,message_count,last_message_at,created_at}
Conversation        = ConversationSummary + {summary:string|null, updated_at}
ConversationMessage = {id,conversation_id,role,parts,refs,created_at}
MessagePart = {type:"text",text}
            | {type:"attachment",sha256,name,mime,size}
            | {type:"requirement_card",requirement_id,title,status,revision}
            | {type:"rollback_proposal",requirement_id,requirement_title,reason}
RequirementTargets = {version_suts:[id],pages:[id],apis:[id]}
RequirementSummary = {id,current_version_id,revision,title,status,priority,targets:RequirementTargets,updated_at,confirmed_at}
RequirementDetail  = RequirementSummary + {current_version:RequirementVersionView,history:[RequirementVersionView],confirmations:[RequirementConfirmation],relations:[RequirementRelation],attachments:[Attachment]}
RequirementVersionView = {id,requirement_id,revision,title,description,acceptance_criteria,priority,constraints,target_version_suts,target_pages,target_apis,content_hash,created_at}
RequirementConfirmation = {id,requirement_version_id,scope,batch_id,actor_id,confirmed_at,affected_requirement_ids}
RequirementRelation = {id,from_requirement_id,to_requirement_id,kind,description,created_at}
Attachment = {id,version_id,requirement_id,requirement_version_id,conversation_message_id,content_ref_id,name,mime,size,extraction_status,extracted_content_ref_id,extraction_error,created_at}
BatchConfirmResult = {batch_id,confirmed:[requirement_id],skipped:[requirement_id]}
TeamRoomView = {team,digest:{summary,pinned_post_ids,token_estimate,version,updated_at}|null,posts:[TeamRoomPost]}
Version detail adds requirement_counts{total,draft,clarified,closed,obsolete}, conversation_count, pending_design_count.
```

- 创建/更新需求请求：`{title,description,acceptance_criteria,priority,constraints,targets:RequirementTargets}`；更新另可带 `reason`。
- 单条确认返回 `{data:{confirmed:bool, requirement:RequirementDetail}}`；批量请求 `{requirement_ids}`，返回 `{data:BatchConfirmResult}`；回退请求 `{reason?,proposal_message_id?}`，返回 `{data:RequirementDetail}`。
- 字段名一旦冻结不得由实现自行改名；以本节 + openapi 的 P1 schemas 为准。

## 9. 事件（P1 追加；详见 contracts/events.md）

频道：`system`（仅版本状态等系统事件）与 `version:{id}`（P1 全部业务事件）；scope 至少含 `version_id`，相关时带 `conversation_id/requirement_id/requirement_version_id/agent_run_id/work_item_id/attachment_id`。
新增类型：`version.started`、`version.status_changed`、`conversation.created`、`conversation.message`、`conversation.summary_updated`、`agent.run.started`、`agent.activity`、`agent.run.finished`、`agent.run.failed`、`context.compacted`、`requirement.created`、`requirement.version_created`、`requirement.clarified`、`requirement.rolled_back`、`requirement.relation_created`、`requirement.attachment_ingested`、`requirement.attachment_failed`、`work_item.created`、`work_item.claimed`、`work_item.failed`、`work_item.dead`、`team_room.post`、`team_room.digest_updated`。
- `conversation.message` payload 含完整 message（id/role/parts/refs/created_at），供 UI 直接渲染；不泄露 prompt/密钥/附件路径。
- 事件写入失败不回滚业务；reconciler 兜底；P1 单实例 Hub，P7 再做 fan-out。

## 10. 前端（`frontend/src/features/clarification/`）

路由：`/versions/{id}?tab=requirements&conversation={cid}&requirement={rid}`；URL 为深链真值。
组件：
- `RequirementsTab`：加载版本、会话列表、当前会话消息、需求列表；订阅 `version:{id}` SSE；事件触发增量刷新；展示加载/空/错误态。
- `ChatPanel`：Header（当前会话、历史对话下拉、＋新增对话）、`MessageList`（用户/assistant/活动气泡、附件 chips、需求内联卡、回退卡）、`Composer`（textarea、Enter 发送/Shift+Enter 换行、附件选择/上传、发送中禁用）、AI thinking 指示（`agent.activity`）。
- `RequirementSidebar`：计数/状态过滤、复选框批量选择、批量确认、需求列表（标题/状态/优先级/target chips）、`RequirementEditor`（标题/描述/验收/优先级/目标 version_sut+页面+API 多选；保存→PATCH；确认→POST；已澄清显示回退提示）、`DesignCTA`（已澄清数>0 时显示 “N 条需求待设计，P2 提供设计对话” 占位）。
- `RollbackCard`：显示 AI 提议原因 + “确认回退/暂不处理”；确认调用 rollback API。
- `AttachmentUploader`：文件 ≤10MiB；上传 `POST /api/content`；上传中/失败状态；随消息发送。
- `RichText`：用 `react-markdown` + `remark-gfm` 渲染 Markdown（禁用 raw HTML，链接安全属性）；不自行实现解析器。
- `TeamRoomPanel`：需求页折叠面板，读取只读团队频道 API，展示澄清团队最新摘要 + 最近帖子（只读，无发帖/控制入口）。
- 错误统一 `ErrorState`/内联提示；发送/确认/保存防重复点击；SSE 断线由 sse-client 自动重连。
- 版本详情其他 Tab 的占位文案按真实阶段修正（设计 P2、执行 P3、链路/问题/报告 P4、代码 P5）。
- 测试：api/types 单测；ChatPanel/Composer/attachment、RequirementSidebar/Editor、RollbackCard、DesignCTA、SSE 合并、批量确认、错误态；Playwright 旅程。

## 11. 契约与 P0 漂移收口

P1 结束冻结：
- `contracts/openapi.yaml`：P1 端点/模型；`x-frozen: P1`。
- `contracts/events.md`：P1 事件追加；保留 P0 语义。
- `contracts/db-schema.md`：P1 表/约束/索引；`migrations-order.md`：7 个迁移顺序。
- `contracts/task-types.md`（新建）：P1 任务类型/team/dedupe/输入/输出/失败语义；P2+ 只列保留类型。
- `contracts/agent-profiles-tools.md`（新建）：P1 profile、工具、scope 注入、schema、提交校验；P2+ 工具标 reserved。
- `contracts/settings.md`：P1 不新增 env/设置键；记录 P1 消费的键（context/agent/team_room/llm）。
- P0 漂移：openapi 的 LLM list envelope/状态码按实现修正（非破坏性、文档与客户端一致）；`eventing_0002` 补 `(channel,created_at)` 索引；`ResetPasswordRequest` 已对齐不再改；SSE `GET /api/stream` 补发契约要求的 `X-Snapshot-Cursor` 头（不改帧协议/游标语义）；`version_sut.scope` 在 DB 存数字 ID、API 用字符串，P1 requirement target 校验统一转 int64。
- **P0 额外漂移（研究发现，P1 处理边界）**：`Event.seq`/`snapshot_cursor` 线上为字符串而契约写整数（D4，**用户已裁决：修代码为整数**，T13 同步 DTO/测试/前端类型）；`auth.login/login_failed/logout`、`user.created/updated` 文档有事件但未发（D6）；未知字段“忽略”文档与 handler 拒绝不一致（D7）；settings 范围/secret PATCH 语义与文档不一致（D8）；`payload_too_large` 未列入稳定错误码（D9）；settings audit/event 顺序（D10）。D6–D10 不在 P1 修，登记 handoff backlog；D11 已在 §3/§12.1 收口。
- **Agent 审计归属**：`audit_log` 不改 schema；agent/后台写操作 `actor_id=0`（系统），`metadata.agent_run_id/work_item_id` 记录来源；事件 scope 带 `agent_run_id`。
- **冻结标记**：openapi 使用文件级 `x-frozen: P1`；其他 contract 文件用顶部状态 `frozen: P1`，P0 的 `x-frozen: P0` 保留在历史段落。
- P0 集成测试期望随 P1 增量更新（迁移数量/表清单、version 频道事件数量、版本详情 fields）；这是测试期望更新，不改变 P0 行为；更新时保留原断言意图并新增 P1 断言。
- 契约更新由 Lead 统一收口；子代理不并发写共享契约。

## 12. 实施决策（缺口/取舍，写入 handoff）

| ID | 决策 | 理由/依据 |
|---|---|---|
| P1-D1 | 阶段按 P1=phase-01 实施，忽略提示词误用的 P0 读取清单 | 权威阶段表 + P0 handoff 已 completed |
| P1-D2 | 新增 `agent` 上下文；需求/对话/附件扩展 `version` 上下文 | Agent.md §6.1 预留 + HLD 垂直域 |
| P1-D3 | 任务板只内部使用，不暴露公开 API | HLD 无任务板 REST 组；P6 Agent 中心再观察 |
| P1-D4 | 租约 30s、心跳 10s、reconciler 3s、max_attempts 3、backoff 2^n 上限 30s；不新增设置键 | 设计给范围（2–5s/10–15s），P1 先常量；避免膨胀设置目录 |
| P1-D5 | 附件增加 extracted_content_ref/status/error/requirement_version_id；旧 .doc/.ppt 不支持 | 运行态所需；现代 docx/pptx 覆盖 doc/ppt；PDF 用 rsc.io/pdf |
| P1-D6 | 需求 priority 枚举 P0/P1/P2/P3，默认 P2 | 设计未给枚举；与 mock/0.1 语义一致，确认时校验 |
| P1-D7 | 需求编辑/回退总是新增不可变 revision；确认记录按 requirement_version 唯一 | D14/D15；自然幂等 |
| P1-D8 | 批量确认事务内全量校验，已确认项幂等跳过；有非法 draft 整体 400 | 避免半成功、UI 易解释 |
| P1-D9 | AI 回退提议只写消息卡，状态变更由用户 API 触发 | D18：AI 不能替用户回退 |
| P1-D10 | Curator/Scout 也是任务板任务；Host/Curator 各自写消息/卡片 | D37/D39；可恢复、可审计 |
| P1-D11 | 聊天不做 token 级流式；用 `agent.activity` + 完整消息事件 | P1 不要求 token streaming；降低复杂度 |
| P1-D12 | P1 stale 只调用端口 + 发事件（affected 为空），P2 接 case 表 | 不提前建 P2 表；phase-01 “P2 消费” |
| P1-D13 | 旧版本 `.doc/.ppt` 拒绝；PDF 文本提取限简单文本；扫描件不做 OCR | 无外部工具/不引入重依赖；handoff 风险 |
| P1-D14 | 前端 Markdown 用 `react-markdown` + `remark-gfm`（禁 raw HTML） | 满足 D48 完整 Markdown；避免自研解析器边界/XSS 风险；新增依赖由 Lead 显式安装并锁版本 |
| P1-D15 | 版本详情增量字段 `pending_design_count` 占位 | P2 design_scope 前提供 CTA；不实现设计 |
| P1-D16 | P1 确认不使用通用 Idempotency-Key，靠唯一约束/自然幂等 | P0 已记录缺口；确认场景已幂等 |
| P1-D17 | P0 三个契约漂移按实现修正/补索引 | P0 handoff §2 指定 P1 收口 |
| P1-D18 | 真实 LLM 验收必跑（`.env` `llm_*`，模型 `deepseek-flash`） | 用户已授权读取 `.env`；验收脚本经 admin API 配置 `llm_config` 并 refresh；API 仍严格 4 项 env；凭据缺失则阻塞并报告，不伪造 |

### 12.1 文档冲突与缺口的落地裁决（不修改设计文档，写入 handoff）

来源编号对应 `.tmp/p1/research/high-level-notes.md` §9；这些是 P1 实现层裁决，不改变 0.2 产品语义。

| # | 冲突/缺口 | P1 裁决 |
|---|---|---|
| Q2 | `current_version_id` vs `current_revision_id` | 用 HLD 的 `requirement.current_version_id`；不建 `current_revision_id` 列；服务端维护指针，编辑/回退后指向新 revision |
| Q3/Q4 | conversation_message vs agent_message；agent_session | UI 线程写 `conversation_message`；LLM/工具 transcript 写 `agent_message`；无 agent_session，conversation 即会话 |
| Q5 | 附件无法挂对话、无解析文本 | `requirement_attachment` 增加 conversation_message_id/requirement_id 可空 + extracted_content_ref_id/status/error；解析文本入 content ledger，不复制全文进需求版本 |
| Q6 | 任务去重/重试语义 | `UNIQUE(version_id,type,dedupe_key)` 全时唯一；重试复用同一行并递增 attempts；需要“再次执行”的新业务用新 dedupe_key（消息 id/附件 id/revision） |
| Q7 | 租约/心跳契约 | 仅内部服务；30s lease/10s heartbeat/epoch fencing；无公开 API；启动只回收过期租约，不批量中断 running |
| Q8 | digest 多版本 | `team_room_digest.version` + `UNIQUE(version_id,team,version)`；保留 `digest_keep_versions` 版，删除更旧 |
| Q9 | summary.version 语义 | 摘要修订号（从 1 递增），`UNIQUE(conversation_id,version)` |
| Q10 | human_request/closure 越界 | P1 不建表/不实现；`waiting_human`/`closed` 状态保留给 P3/P4，P1 不写 |
| Q11 | 回退提议无模型/接口 | 提议 = conversation_message part `rollback_proposal`（含 requirement_id/reason/created_at）；用户确认走 `POST .../rollback` |
| Q12 | 事件缺口 | 追加 P1 事件（§9）；token 增量不做（P1 用 activity + 完整消息）；事件只追加不改 P0 |
| Q13 | agent_profile 字段冲突 | 用 HLD §2.2 的 `budgets JSONB` + `model/allowed_tools/enabled`；不建逐列 max_turns 等 |
| Q14 | draft 编辑语义 | 所有编辑生成新不可变 revision（draft→draft revision+1）；clarified 编辑同时 status→draft；确认后不可再改该 revision |
| Q16 | chat.turn vs clarify.turn | 用 HLD/phase-01 的 `chat.turn`；`clarify.turn` 不创建 |
| Q17 | 迁移示例 clarification_session | 不采用；按设计表 `conversation`，迁移名 `agent_0001/0002/0003`、`version_0002/0003/0004`、`eventing_0002` |
| Q18 | 版本投影归属 | version service 在首条对话消息/首个 requirement/首次确认时写 running；reconciler 兜底修正；P1 不写 completed |
| Q19 | reconciler 多实例/设置 | P1 单实例（与 P0 Hub 一致）；3s 常量扫描；P7 再做 leader election/多副本 |
| Q20 | design 待办占位数据源 | `pending_design_count = 已澄清需求数`（P1 无 design_scope）；UI 只显示 CTA 占位，不建设计任务 |
| Q21 | Idempotency-Key | P1 不建 `idempotency_key` 表；确认/消息用唯一约束与状态幂等；通用中间件留 handoff |
| Q22 | context_compacted 语义 | 过程事件/恢复路径，不是失败终态；只有不可恢复的 context_overflow 才 failed（terminal_reason=context_overflow_unrecoverable） |
| Q23 | 上下文预算模型 | **优先从 LLM 网关模型目录读取 `context_window/max_output`**；缺失回退 `context.window_tokens/output_reserve_tokens`；token 估算保守（runes/2）；逐模型元数据随 refresh 更新 |
| Q24 | 需求字段类型/枚举 | title/description/acceptance 文本；priority P0–P3；constraints JSON 子键 business_rules/preconditions/exceptions/data_constraints/dependencies；content_hash = 规范化 JSON 的 sha256 hex |
| Q25 | 稳定错误码 | 新增 `state_conflict/lease_lost/ai_unavailable/context_overflow/unsupported_attachment_format/extraction_failed`；HTTP 映射见 §8.3 |
| Q26 | 权限 | P1 单团队：所有登录用户可读写需求/对话/确认（沿用 P0 版本读写），无 RBAC；admin 仅 P0 资源/设置 |
| Q27 | 前端数据缺口 | API 提供 requirement_counts/conversation message_count/last_message_at/attachment extraction_status/rollback_proposal；上下文百分比不提供（P1 不做 token 流） |
| Q28 | 0.1 P1 spec 陷阱 | 不读取/不沿用 `docs/superpowers/specs/2026-10-27-eztest-p1-agent-runtime-design.md` 的 0.1 控制面假设 |
| — | work_item 状态数 | 按设计 7 态（含 `waiting_human`）；P1 只写 open/claimed/running/done/failed/dead，不写 waiting_human |
| — | agent 审计 | `audit_log.actor_id=0`（系统）+ metadata `agent_run_id/work_item_id`；不改 P0 audit schema |
| — | 冻结标记 | openapi 文件级 `x-frozen: P1`；其他契约文件顶部 `frozen: P1` |
| D4 | Event.seq 线上字符串 vs 契约整数 | **用户裁决：修代码为整数**；T13 同步 event DTO/handler 测试/前端类型；契约保持整数 |

## 13. 测试与验收计划

| 层 | 覆盖 | 命令/证据 |
|---|---|---|
| Go 单测 | work_item 状态/租约/重试/fencing；reconciler 幂等；token 预算/压缩/overflow；工具 schema/scope；需求状态机/确认/回退/stale hook；附件解析（md/txt/docx/pptx/pdf 生成夹具）；摘要触发 | `cd backend; go test ./...` |
| Go 集成 | 迁移幂等；任务并发领取/租约回收/旧 epoch 拒绝；HTTP API 权限/校验/幂等；SSE 事件；附件 ingest；对话→需求全链路；长会话压力 | `go test -tags=integration ./...`（`EZTEST_TEST_DATABASE_URL`） |
| 前端单测 | API/类型、Chat/Composer/上传、需求列表/编辑器/批量确认/回退卡/CTA、SSE 合并、错误态 | `pnpm lint && pnpm typecheck && pnpm test` |
| E2E | fake LLM：登录→版本→新建对话→AI 回复→draft 需求→编辑→确认→批量确认→回退→附件上传→设计 CTA；无 console error | `scripts/p1-e2e.ps1` |
| API 验收 | 不依赖浏览器：任务板并发/恢复、对话/需求/确认/回退/SSE/附件 | `scripts/p1-acceptance.ps1` |
| 压力 | 200 条消息 + 大附件 + 摘要保留关键事实；任务 50 并发领取无重复 | `go test -tags=integration ./internal/agent/... ./internal/version/... -run 'Stress|Concurrent'` |
| 真实 LLM | **必跑**：读取 `.env` 小写键 `llm_api_key`、`llm_base_url`（默认 `https://api.deepseek.com`）、`llm_model_name`（默认 `deepseek-flash`）；脚本经 admin API 写 `llm_config`/refresh 后跑澄清闭环；模型窗口从网关目录读取；API 不读这些键 | `scripts/p1-acceptance-llm.ps1` + `.tmp/p1-acceptance/llm.log` |

**验收清单（映射 phase-01）**
- [ ] 新对话 → Host 读上下文/附件 → Host 回复 + Curator draft 需求。
- [ ] 编辑/单条/批量确认 → 状态/确认记录/事件正确；AI 回退卡 → 用户确认 → 新 draft revision；stale 钩子调用。
- [ ] 并发领取不重复；租约过期恢复；旧 epoch 写回 409；重复扫描不重复任务。
- [ ] 上下文压缩保留 pinned/最近窗口/任务约束；`context_overflow` 场景不失败。
- [ ] 附件 md/txt/docx/pptx/pdf 解析；损坏/不支持格式可解释失败。
- [ ] 版本 initial→running（首条消息/首个需求/首次确认）；requirements Tab、SSE、CTA、团队频道只读面板可见。
- [ ] 真实 LLM 验收通过：`.env` `llm_*`（`deepseek-flash`）；凭据缺失则 handoff 标 blocked，不以 fake 结果替代。
- [ ] `scripts/test.ps1` 全绿；E2E/验收通过；契约冻结；handoff 完成。

## 14. 风险与已知缺口（写入 handoff）
1. 上下文摘要质量/中文 token 估算偏保守；P1 用确定性回退保证不丢关键事实，真实 LLM 质量待 P8。
2. 旧 `.doc/.ppt`、扫描 PDF、图片视觉理解不在 P1；现代 OOXML/文本 PDF 覆盖主路径。
3. P1 单实例 Hub/worker；多副本 lease/fencing 原语保留，完整部署 P7。
4. stale 只到端口/事件，P2 接 case 表后才能端到端验证“用例 stale”。
5. 真实 LLM 验收使用 `.env` 的 `deepseek-flash`；若缺少 `llm_api_key`，验收阻塞并在 handoff 明确，不以 fake LLM 结果冒充真实模型质量。模型窗口来自网关目录，缺失回退设置并记录。
6. 任务板无公开 API/UI；P6 Agent 中心再暴露；P1 靠测试与数据验证。
7. P0 开发库含冒烟数据；P1 演示用独立库/清库重跑，避免名称冲突。
