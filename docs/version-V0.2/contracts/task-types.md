# EzTest 0.2 P1 任务类型契约

> 状态：`frozen: P1`（2026-10-31；P2+ 只允许增量）。
> 权威：`docs/version-V0.2/high-level-design.md` §3、`architecture-design.md` §5.3、`phases/phase-01-runtime-clarification.md`。
> 实现：`backend/internal/agent`（work_item/worker/reconciler）。P2+ 类型只保留，不在 P1 创建。

## 1. 通用模型

`work_item` 是唯一执行队列；状态：

`open → claimed → running → done | failed | dead`；`running → waiting_human`（P3 登录接管使用）；`failed → open`（重试）或 `dead`（不可重试/超限）。

| 字段 | 语义 |
|---|---|
| `version_id` | 版本作用域 |
| `team` | `clarification / design / execution / delivery / steward / code`（`code` 为 P5 增量） |
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

## 3. P2 任务类型（实现；`frozen: P2`，2026-10-31）

| type | team | profile | dedupe_key | payload | 输出 | 失败语义 |
|---|---|---|---|---|---|---|
| `design.round` | design | `design_planner` | `round:{conversation_id}` | `{conversation_id,round_id}` | 本轮范围说明（assistant message + 团队帖） | 模型不可用可重试；作用域错误 dead |
| `design.requirement` | design | `case_author` | `design:{requirement_id}:{requirement_version_id}` | `{requirement_id,requirement_version_id,reason}` | 场景/用例 draft（`case.graph_propose`）+ 团队帖 | 结构化校验失败可重试；越权/跨版本 dead |
| `design.gap` | design | `case_author` | `gap:{requirement_id}:{case_version_id|requirement_version_id}` | `{requirement_id,case_id?,case_version_id?,reason}` | 新 draft（`case.graph_propose`）+ 团队帖 | 同上 |
| `design.critic` | design | `design_critic` | `critic:{requirement_id}:{requirement_version_id}` | `{requirement_id,requirement_version_id}` | `design.review_submit` finding + 团队帖 | 模型不可用可重试 |
| `design.data_plan` | design | `data_planner` | `dataplan:{requirement_id}:{requirement_version_id}` | `{requirement_id,requirement_version_id}` | `design.data_plan_submit` 更新 draft + 团队帖 | 仅 draft 可更新；状态冲突不重试 |

- `chat.turn` 继续服务澄清与设计 Host：`conversation.kind='design'` 时 team=design、profile=`design_host`；澄清保持 P1（team=clarification、clarification_host）。这是语义增量，不新增 type。
- 创建者：设计对话创建/轮次扫描时 ensure `design.round` 与 `design.requirement`；reconciler 兜底 pending/auto/变化/stale；`design.gap` 由执行侧 `RequestGap` 或 reconciler ensure；critic/data_plan 由 design reconciler 在存在 draft/confirmed 时 ensure。
- 重试/租约/backoff/dead 语义沿用 P1；design 任务不写 `waiting_human`。
- `case.confirm` 不是任务、也不是模型工具；确认只走用户/服务端 API。

## 4. P4+ 保留类型（不实现、不创建）

`report.render_pdf`、`knowledge.review`、`gc.run`、`visual.*`、`search.*`。

P4 已把 5 个预留类型移入 §6 并实现；P5 把 `index.repo` 移入 §7 并实现（`code.analyze` 为 P5 新增类型，非预留）；`report.render_pdf` 仍保留、不注册（PDF 属 P7）。`visual.*` 不是独立任务类型：视觉断言在用例执行任务内执行（见 §7 与 `executor-protocol.md` v1.2），`search.*` 仍保留。其余保留类型在后续阶段写入本文件并扩展 work_item/worker；当前阶段不注册对应 profile、不创建任务。

## 5. P3 任务类型（实现；`frozen: P3`，2026-11-01）

P3 把 `execute.case`、`analyze.failure`、`analyze.coverage` 三个类型从 §4 移到本节并实现。

| type | team | profile | dedupe_key | payload | 输出 | 失败语义 |
|---|---|---|---|---|---|---|
| `execute.case` | execution | `case_executor` | `execute:{intent_id}:{dispatch_epoch}` | `{intent_id,wave_id,case_run_id,case_version_id,dispatch_epoch,dispatch_token}` | step/assertion/evidence/case_run 终态 | 模型/工具失败按 P1 retry；scope 失效 dead；lease/epoch 冲突 409 `lease_lost`（不覆盖） |
| `analyze.failure` | execution | `failure_analyst` | `failure:{wave_id}` | `{wave_id,case_run_ids[]}` | 分类建议 + 问题候选帖/事件 | 可重试；最终失败不阻塞 wave 终态（确定性兜底已写） |
| `analyze.coverage` | execution | `coverage_analyst` | `coverage:{wave_id}` | `{wave_id}` | 缺口帖/事件 + `design.gap` ensure | 可重试；最终失败不阻塞 wave 终态 |

- `analyze.wave` **不注册**（ARCH §5.3 为简写）；`report.wave`/`report.version`、`issue.triage`、`version.close` 不在 P3。
- 执行 Planner 是确定性 runtime（ARCH §7.1），不注册 profile（P3-D-15）。
- 创建者：调度器在派发 case_run 时 ensure `execute.case`（dedupe 含 `dispatch_epoch`，重派生成新任务）；wave 进入 `done`/`failed` 终态时 ensure `analyze.failure`/`analyze.coverage`（`cancelled` 不 ensure，P3-D-26）；P2 design reconciler 消费 `design.gap`（已有）。
- `execute.case` 的租约/心跳沿用 P1：lease 30s、heartbeat 10s；runner 写结果/证据必须匹配 `dispatch_epoch+dispatch_token` CAS。
- 恢复语义：offline/lease 过期 → 同 case_run `interrupted→pending`（attempts+1、epoch+1，最多 3 次）→ 新 `execute.case`；旧 epoch 写回 409。
- `analyze.*` 任务在 wave 终态后由确定性兜底先写 `failure_class`/`summary`/候选事件/`design.gap`，Agent 只做增强与解释；Agent 失败不回滚。

## 6. P4 任务类型（实现；`frozen: P4`，2026-11-01）

P4 把 `issue.triage`、`report.wave`、`report.version`、`requirement.close`、`version.close` 五个类型从 §4 移到本节并实现（P4-D-01/P4-D-02/P4-D-06）。

| type | team | profile | dedupe_key | payload | 输出 | 失败语义 |
|---|---|---|---|---|---|---|
| `issue.triage` | delivery | `issue_triager` | `triage:{issue_id}` | `{issue_id,version_id}` | `delivery_analysis_run`（done/degraded）+ 团队帖 + 合并/分诊建议（不改 issue 状态） | 可重试；最终 dead 不影响 issue/报告 |
| `report.wave` | delivery | `report_writer` | `report:{report_id}:r{revision}` | `{report_id,wave_id,revision}` | 新 `delivery_report_revision`（HTML published）+ 事件 | 可重试；失败时 report/revision 保持 draft/failed，reconciler 补；不伪造 |
| `report.version` | delivery | `report_writer` | `report:{report_id}:r{revision}` | `{report_id,version_id,revision}` | 新 `delivery_report_revision`（HTML published）+ 事件 | 可重试；版本收尾被阻断直到 published |
| `requirement.close` | steward | `version_steward` | `req-close:{requirement_id}:{requirement_version_id}:{close_round}` | `{requirement_id,requirement_version_id,close_round}` | `requirement_closure` + requirement `closed` + 事件 | 校验失败 409（可重试）；最终 dead 可见；版本收尾阻断 |
| `version.close` | steward | `version_steward` | `ver-close:{version_id}:{report_revision_id}:{predicate_fingerprint}` | `{version_id,report_revision_id,predicate_fingerprint}` | version `completed` + `closed_at/close_reason/close_run_id` + 事件 | 事务复核失败 409；最终 dead 可见；不自动降级 |

- **创建者**：reconciler 检测到 wave 终态且无 published wave report → 创建 report 行 + ensure `report.wave`；版本报告条件满足 → ensure `report.version`；issue 候选创建后 ensure `issue.triage`；需求闭环条件满足 → ensure `requirement.close`；版本 close 谓词满足 → ensure `version.close`；API `regenerate` 计算 `revision = max+1` 后 ensure 新任务。重复 ensure 返回既有行，不新增。
- `report.render_pdf` 继续保留、**不注册**（P7）。
- 任务租约/心跳/退避/`dead` 沿用 P1/P3；`requirement.close`/`version.close` 不写 `waiting_human`。
- `version.close` 任务自身从版本 close 谓词的“无开放任务”中排除；`predicate_fingerprint = sha256(canonical(version_id, requirement 状态集合, report_revision_id, open_counts...))`，状态变化自然产生新 dedupe key。
- `report.wave`/`report.version` 的 revision 在任务创建时确定；同一 next revision 的活跃任务存在时幂等返回 202；旧 revision 不删除。
- Agent 输出只作候选：`issue.propose` 不改 issue 状态；`report.narrative_propose` 不改统计数字；`requirement.goal_met`/`version.close_propose` 由服务端事务内重算校验，失败返回 409 带失败项（见 `agent-profiles-tools.md` §7）。

## 7. P5 任务类型（实现；`frozen: P5`，2026-11-01）

P5 把 `index.repo` 从 §4 移到本节并实现，并新增 `code.analyze`；两者 team=`code`，由 **codeintel 专用 worker** 领取，不映射 Agent profile（P5-D-03/B1a）。

| type | team | profile | dedupe_key | payload | 输出 | 失败语义 |
|---|---|---|---|---|---|---|
| `index.repo` | `code` | —（不映射 Agent profile） | `code:index:{index_run_id}` | `{repo_id,commit_sha,branch,index_run_id}` | `code_index_run` 终态（done/failed）+ `code.index_started/finished/failed` 事件 | 可重试错误重新 `open`；超过 `max_attempts` → `dead` 且 run 置 `failed`；不可重试 → `failed/dead`；同一 run 重跑幂等（不重复实体/边） |
| `code.analyze` | `code` | —（不映射 Agent profile） | `code:analyze:{analysis_id}` | `{analysis_id,repo_id,base_commit_sha,head_commit_sha}` | `code_analysis` 终态（done/failed，可 degraded）+ candidates/unmapped + `code.analysis_*`/`code.impact_computed` 事件 | 同上；LLM 不可用 → `done + degraded`（不伪造事实）；同一 run 重跑不重复事实/候选 |

- 两者 `version_id` = 触发版本；`work_item.version_id` 保持 NOT NULL（codeintel 任务必须版本作用域，B1a）。`max_attempts=3`；`priority=50`。
- 领取语义（`agent-profiles-tools.md` §8）：`WorkQueue.ClaimForTeams(ctx, claimant, teams)` 增量；codeintel worker 用 `["code"]`，agent worker 用其余 5 个 team（排除 `code`）。
- 创建者：`POST /api/versions/{versionId}/code/index` 在同事务内建 `code_index_run` 并 ensure `index.repo`；`POST .../code/analyses` 建 `code_analysis` 并 ensure `code.analyze`；重复 ensure 返回既有行（同 dedupe key），不新增。
- 事件：`work_item.created/claimed/failed/dead` 由 P1 worker 发布到 `version:{id}`；P5 不为 `code` 团队建 `team_room`/digest（§4.4）。
- 完成语义：业务产物写入成功且事件发布尝试完成后 `done`；事件失败不回滚业务，reconciler 兜底；失败不阻塞其他 version 任务。
- `visual.compare` 不是 work_item 类型：由 `execute.case` 内的 `execution.visual_assert` 工具/执行流程调用执行器 action（见 `agent-profiles-tools.md` §8、`executor-protocol.md` v1.2）。

## 8. P7 任务类型（实现；draft: P7，2026-11-01）

P7 把 `report.render_pdf` 从 §4 的预留类型移入本节并实现；team=`delivery`，版本域，不映射 Agent profile（非模型任务，由每实例 `RenderWorker` 领取）。

| type | team | profile | dedupe_key | payload | 输出 | 失败语义 |
|---|---|---|---|---|---|---|
| `report.render_pdf` | delivery | —（不映射 Agent profile） | `report:{report_id}:rev:{revision}` | `{report_id,report_revision_id}` | 读已发布 HTML（内容账本）→ renderer PDF → 写 `content_ref`（owner_kind=`report_revision`、role=`report_pdf`）→ fenced 更新 `pdf_status='done'`/`pdf_ref`/`pdf_rendered_at`/`pdf_attempts` + 事件/审计 | 可重试错误 → `Fail(retryable=true)` 回 `open` 指数退避；超过 `report.render_max_attempts` 或不可重试 → `pdf_status='failed'` + `report.pdf_failed`；不影响报告 published 与版本收尾 |

- **版本作用域**：`version_id` = 报告所属版本（`work_item.version_id` NOT NULL）；payload 只带引用，不带 HTML/PDF 字节。
- **去重**：`dedupe_key = report:{report_id}:rev:{revision}`（全时唯一）；同一 revision 重复 ensure 返回既有行，不新增；`revision` 取 `delivery_report_revision.revision`。
- **重试**：`max_attempts` 取设置 `report.render_max_attempts`（1–10，默认 3），不使用 work_item 默认 3；租约取 `report.render_lease_seconds`（30–1800，默认 120）；worker 用 `ClaimForTypes(ctx, claimant, ["report.render_pdf"], lease)` 领取并按类型续租。
- **创建者**：`ReportService.Publish` 成功后置新 revision `pdf_status='pending'`（`report.render_pdf=false` 时 `disabled`）并 ensure；P7 reconciler（`P7Ensurer`）扫描 `published + render_status='done' + pdf_status IN ('pending','failed') + pdf_attempts < report.render_max_attempts` 的 revision 兜底 ensure（含迁移后既有 revision 补齐）；`report.render_pdf=false` 时跳过。
- **并发/安全**：同一 revision 至多一个活跃任务（dedupe + work_item CAS）；结果写回必须匹配 `lease_epoch`，PDF 元数据更新需 fenced（`pdf_ref` 为空且 `pdf_status IN ('pending','failed','running')`）；HTML 读取校验内容账本 owner/role/sha/size。
- P7 **不新增其他任务类型**：`knowledge.review`/`gc.run`/`search.*` 仍保留、不注册；知识审核走 admin API，周期 GC 是 leader worker 而非 work_item。P7 后任务类型合计 **19**（P1 3 + P2 5 + P3 3 + P4 5 + P5 2 + P7 1）。
