# EzTest 0.2 架构设计（讨论稿）

> 状态：讨论稿，基于 2026-10-31 已确认的设计决策。
> 目标读者：架构师、后端/前端/Agent 工程师、测试与运维。
> 关联材料：
> - [设计决策日志](research/decision-log.md)
> - [0.1 能力矩阵与 0.2 处置](research/capability-matrix.md)
> - [临时 UI 草图](ui/mock.html)
> - 0.1 现状：[architecture-design.md](../architecture-design.md)、[detailed-design.md](../detailed-design.md)
>
> 本文件不是需求规格、不是实施计划、不是任务拆分；它定义 0.2 的架构骨架、边界与关键取舍。

---

## 1. 0.1 诊断与 0.2 命题

### 1.1 0.1 为什么"UI 对了、流程不可用"

0.1 的页面和主流程基本按设计实现，但控制面是"人点按钮 + 确定性调度器 + 单次 Agent run"：

| 根因 | 表现 | 证据 |
|---|---|---|
| **状态机驱动流程** | 版本 8 状态（draft/clarifying/designing/reviewing/executing/continuing/completed），每步靠人工动作推进；对话不能驱动流程 | `version/domain/version.go:11-21`；`clarification_service.go:167-175`；前端 `VersionHeader.tsx:205-215` |
| **没有团队运行时** | 没有 Agent 任务池、认领、租约、团队频道；Agent run 由 HTTP 或后端 worker 直接创建；设计→执行没有自动交接 | `02-runtime-code-analysis.md` §2；`01-architecture-synthesis.md` |
| **Agent 是"功能点"** | 澄清/设计/执行各是一个 profile，用户不点就不动；设计团队不主动检索澄清产物 | `clarification_service.go:270-306`；`mindmap/run_service.go:381-518` |
| **上下文是"全量回放 + 截断"** | 最新 user 组永不裁剪、工具结果可堆 30×32KiB、窗口 token 当字符用、附件入口可注入 200KiB；context_overflow 直接判 run 失败 | `context_builder.go:49-105`；`runner.go:195-209`；`02-runtime-code-analysis.md` 专节 |
| **模型不知道运行域** | 澄清 prompt 要求调 `version.context_get`，但工具 schema 强制 `versionId`，会话没有把 versionId 注入上下文，Agent 只能反问用户 | `profile_p3.go:89`；`clarification.go:91`；`run_service.go:167-170` |
| **用例模型不对** | 脑图根是场景、整版基线冻结、只有整版评审，没有逐用例确认；执行要人建批次 | `mindmap` P5 模型；`review_service.go:150-241`；`batch_service.go:112-226` |
| **配置负担** | 53 个 `EZTEST_*` 环境变量，大量能力未接线；与"开发测试环境一键用"冲突 | 0.1 `detailed-design.md` §10.2 |

结论：0.1 不是缺功能，而是**控制面错了**。0.2 必须把"人/阶段状态机驱动"换成"Agent 团队按共享任务板自主推进"。

### 1.2 0.2 核心命题

> **人只在三个点介入：澄清对话、开启设计对话、确认用例。其余过程由 Agent 团队自主推进；版本状态只是对外投影，不是门禁。**

0.2 同时是**完整替代版**：对齐 0.1 P0–P8 的全部已交付能力，并补齐 P9 的 D59/D63/D65/D67 与问题重开/合并/跨波次去重。

---

## 2. 目标、非目标与成功标准

### 2.1 目标

1. **AI 驱动**：澄清、设计、执行、交付都由团队运行时自主推进；人不开会式点每一步。
2. **对话优先**：需求澄清与用例设计的主 UI 是 AI 对话；列表/脑图/详情是辅助。
3. **半自主边界清晰**：人只做澄清、开设计轮次、确认用例；异常（登录接管等）走例外通道。
4. **多 SUT / 跨系统**：一个版本可绑定多个 SUT；一条用例可跨 SUT 执行；页面/API 清单是一等资源。
5. **可追溯**：需求版本 → 用例版本 → 执行波次/结果 → 证据 → 问题 → 报告 → 需求闭环，全链路可查。
6. **长跑不崩**：任务域上下文 + 滚动摘要 + 钉住事实 + 工具结果落盘；context_overflow 不再是终态。
7. **极简启动配置**：`.env` 只保留 4 项，其余进系统设置中心。
8. **完整替代**：0.1 P0–P8 能力回归 + P9 五项。

### 2.2 非目标（0.2 不做）

- 多用户协作/读写锁/站内通知（0.1 P9 候选，不在 0.2 清单）。
- 非 PostgreSQL 目标库断言。
- 外部工单/CI/IM/Webhook 集成。
- 多租户、复杂 RBAC、SSO。
- 生产级安全加固、密钥托管、多区域 HA、自动扩缩容。
- 跨版本问题合并、非语义化的自动合并。
- 0.1 接口/数据兼容（0.2 新 schema；旧数据只读归档/导出）。

### 2.3 成功标准

1. 从版本创建到需求闭环，用户只做三个半自主动作，其余无需点击。
2. 新增需求 → 澄清 → 开设计对话 → 确认用例 → 执行 → 问题/报告 → 版本收尾，全链路可演示。
3. 多 SUT 版本中，跨系统用例可稳定执行并产出分 SUT 证据。
4. 长会话/长执行不再出现 `context_overflow` 失败；有摘要、压缩、落盘与恢复记录。
5. 0.1 P0–P8 能力全部有 0.2 对应入口与验收路径。
6. `.env` 只有 4 项；管理员可在 UI 完成其余配置。
7. 探索链路可实时看到需求→闭环，支持 SUT/页面过滤与历史回放。

---

## 3. 设计原则

1. **控制面与执行面分离**：任务板决定"谁该做什么"，Agent 只负责"怎么做"；两者不混在一个循环里。
2. **状态是投影，不是门禁**：`version.status` 只服务 UI/报告；真正推进依据是任务板与实体状态。
3. **作品与过程分离**：需求版本、用例版本、证据、问题、报告是持久作品；Agent run 是一次性的过程。
4. **AI 只提交候选，服务端校验后落库**：沿用 0.1 的 A08 原则；AI 不直接确认需求/用例、不关闭版本、不合并问题。
5. **任务域上下文**：每次 run 重建上下文；不把整个版本/整个聊天历史塞进 prompt。
6. **单一真值 + 事件投影**：DB 是唯一真值；事件/SSE 是 UI 投影，事件丢失由 reconciliation 兜底。
7. **团队频道不等于消息总线**：频道是结构化协作/观察层，不直接触发 Agent；跨团队交接走任务板 + 产物引用。
8. **多 SUT 是一等维度**：从版本绑定、用例步骤、执行上下文、证据、报告、链路都必须能表达 SUT。
9. **配置最小化**：env 只负责启动，运行配置进 DB 设置中心。
10. **开发测试信任模型**：凭据 AI 可读、不做密钥托管；用受控网络/权限/审计约束，而不是加密工程。

---

## 4. 系统上下文与容器

```text
┌──────────────────────────────────────────────────────────────────────┐
│ Web（Next.js/React）                                                  │
│ 全局壳：版本/问题/报告/知识/资源/质量/Agent 中心/设置                 │
│ 版本详情：总览/需求与澄清/用例设计/执行/探索链路/问题/报告/代码与变更  │
└──────────────────────────────┬───────────────────────────────────────┘
                               │ REST + SSE（快照游标 + 事件回放）
┌──────────────────────────────▼───────────────────────────────────────┐
│ API/服务（Go 模块化单体）                                              │
│ 身份/资源/版本/需求/设计/执行/交付/知识/质量/设置/事件/审计/LLM 网关    │
│ 团队运行时：任务板 + reconciler + 租约 + 团队频道 + 上下文构建         │
│ 执行调度：波次/用例 run/多 SUT 上下文/证据                             │
└───────────────┬───────────────────────┬──────────────────────────────┘
                │                       │
        ┌───────▼────────┐      ┌───────▼────────┐
        │ PostgreSQL     │      │ 文件存储        │
        │ 业务/任务/事件  │      │ 附件/证据/导出/ │
        │ 设置/审计       │      │ 报告 HTML/PDF  │
        └────────────────┘      └────────────────┘
                │
        ┌───────▼────────────────────────────────────────────┐
        │ 执行器（Node + Playwright，可多 SUT 上下文）          │
        │ 浏览器上下文/动作/截图/网络/下载；不接触 LLM 上下文    │
        └────────────────────────────────────────────────────┘
外部：LLM 供应商 · 被测 SUT（多系统/多环境/多页面） · 可选 PG 目标库
```

**部署形态**：
- 单机默认：API + Web + PostgreSQL + Executor；可打包运行，也可 Compose。
- 多副本安全：API 可多副本；任务板/租约/事件通过 PostgreSQL 协调；文件存储需共享（NFS 或等价物）。
- 执行器可单实例多上下文，或多实例注册；0.2 先做"多副本安全"，完整编排在 P9 波次。

---

## 5. 总体架构

### 5.1 三个面

| 面 | 组成 | 职责 |
|---|---|---|
| **交互面** | 全局壳、版本工作台、对话、确认卡、链路/报告/问题页 | 人输入、确认、观察；不直接推进流程 |
| **控制面** | 任务板、reconciler、planner、租约、团队频道、人机请求 | 决定"谁在什么时候做什么"；可恢复、可去重、可审计 |
| **数据/执行面** | 需求/用例版本、执行波次/结果、证据、问题、报告、知识、LLM/执行器 | 持久作品与真实执行；服务端校验后写入 |

三者关系：交互面写入"人意图/确认" → 控制面生成/领取任务 → 执行面产出作品与事件 → 交互面观察。

### 5.2 团队运行时

**四个团队 + 一个收尾角色**：

| 团队 | 触发方式 | 主要工作 |
|---|---|---|
| 澄清团队 | 用户"新增对话"、附件上传 | 澄清 Host 对话、需求抽取/改写、上下文侦察 |
| 设计团队 | 用户"新建对话"开启设计轮次；执行缺口/需求变化自动补 | 设计 Host、规划本轮范围、生成/修改脑图、内部自审、数据前置 |
| 执行团队 | 已确认用例出现，planner 自动组波 | 波次规划、用例执行、失败分析、覆盖缺口 |
| 交付团队 | 波次结束、问题/报告/知识任务 | 问题分诊、报告、知识沉淀、版本收尾提议 |
| Version Steward | 版本无开放工作项时 | 结构化 `version.close_propose` 提议 + 服务端校验（任务类型 `version.close`） |

**关键约束**：
- 团队之间不直接调用；交接 = 写作品 + 任务板出现新任务。
- planner 只规划，不执行业务动作；worker 只领一个任务，结构化写回。
- Host 只做人机对话，不直接确认/关闭。
- 所有 Agent 都必须通过任务板获得工作，聊天消息也先变成 `chat.turn` 任务。

### 5.3 任务板与租约

`work_item` 是唯一执行队列：

| 字段 | 说明 |
|---|---|
| `id` / `version_id` | 主键 / 版本作用域 |
| `team` / `type` | 团队与任务类型（clarify.turn/design.round/case.author/execute.case/analyze.failure/report.build/version.close…） |
| `status` | `open / claimed / running / waiting_human / done / failed / dead` |
| `priority` / `available_at` | 优先级与可执行时间 |
| `dedupe_key` | 幂等键；唯一索引防止重复任务 |
| `depends_on` | 依赖任务（可空） |
| `payload` | 任务参数（只放引用与小数据，不放全文） |
| `claimed_by` / `lease_until` / `lease_epoch` | 认领者/租约/栅栏令牌 |
| `attempts` / `max_attempts` / `last_error` | 重试与死信 |

**领取**：`UPDATE work_item SET status='claimed', claimed_by=..., lease_until=..., lease_epoch=lease_epoch+1 WHERE id=(SELECT ... FOR UPDATE SKIP LOCKED) RETURNING ...`。长任务心跳续租；实例崩溃后租约过期被回收；`lease_epoch` 防止旧实例写回。

**reconciler**：
- 每 2–5 秒扫描"期望状态 vs 实际状态"，生成缺失任务（不是用事件直接触发业务）。
- 事件用于低延迟唤醒；漏事件由扫描兜底。
- 关键 desired-state 规则：
  - 已澄清需求版本 + 无设计覆盖/设计 stale → `design.requirement`
  - current+confirmed 用例版本 + 环境/代码基线无 active intent → `execution_intent`
  - 波次终态 + 未分析 → `analyze.wave`；未出波次报告 → `report.wave`
  - 需求/波次全部闭环 + 无 open intent + 无 version 总结报告 → `report.version`
  - 版本 `status=running` + 无 open/claimed/running/waiting_human work item + 无 open/claimed/running intent + 无活跃/暂停波次/在飞 case_run + 无 open human_request + 需求全 closed/obsolete + 版本总结报告 published → `version.close`
  - `completed` 版本只扫描“新需求/需求重开/新确认/新 intent”并清完成标记；不重复 ensure close
  - 执行缺口 → `design.gap`
  - 知识晋升/问题合并 → 对应任务

### 5.4 团队频道

- 每个 `版本 × 团队` 一个频道，持久化 `team_room_post`。
- 帖子类型：`finding / decision / blocker / handoff / status`；可带 `refs`（需求版本、用例版本、证据、问题、任务）。
- 频道是**结构化协作层**：planner 读摘要 + 相关帖子；worker 读取与本任务相关的帖子；人类可进入对话。
- **不直接触发 Agent**；人类 `@` 只生成 `work_item`，由 planner/Host 处理。
- 频道摘要进入上下文包，但不全文回放。默认每 20 条或 10 分钟生成，保留最近 N 版；帖子全量审计；不做自由群聊控制面。

### 5.5 事件与 SSE

- DB 为真值；`event` 表持久化，SSE 先快照游标再回放（沿用 0.1 的 A04 思路）。
- 多副本：PG `LISTEN/NOTIFY` 或事件轮询把新事件 fan-out 到各实例的 SSE Hub；`stream.reset` 处理游标过旧。
- 事件只做 UI 投影与低延迟唤醒；不承担可靠任务传递。
- 事件 envelope 保留 `version_id/requirement_id/case_id/wave_id/case_run_id/agent_run_id/version_sut_id/page_id/api_id` 等作用域字段；执行域用 `version_sut_id`。

---

## 6. 领域模型

### 6.1 资源与版本绑定

```text
SUT（系统）
 ├─< SUTEnvironment（环境入口：dev/staging，baseUrl）
 │    ├─< SUTPage（页面链接：名称/URL/路由/说明/登录角色/模块）
 │    └─< SUTApi（API 端点：method/path/说明/登录角色/来源）
 ├─< TargetDatabase（目标库：类型/DSN/凭据/只读）
 ├─< TestAccount（测试账号：账号/密码/角色/环境）
 └─< CodeRepository（代码仓：路径/分支/排除规则）

Version
 ├─< VersionSUT（绑定的 SUT + 环境 + 代码仓）
 │    └─< VersionResource（该 SUT 下选中的目标库/测试账号）
 ├─< Requirement（逻辑需求）
 ├─< Conversation（澄清/设计对话）
 └─< RunWave（执行波次）
```

- 一个版本可绑定多个 SUT；每个 `VersionSUT` 选一个环境入口、一个代码仓（可扩展多仓）。
- 应用模型（页面/API）属于 SUT/环境；版本绑定 SUT 时选择页面/API 范围（默认全部，可收窄）。
- 页面同时存完整 URL 与 route_template；API 存 method + path_template；AI 探索只提议，管理员确认后入库；去重按 template；页面/API 变更不自动使全部用例 stale，由设计团队评估。
- 测试账号密码 AI 可读；UI 不回显；访问审计。
- 代码仓由任意登录用户创建；版本绑定时选择。

### 6.2 需求与澄清

```text
Requirement（逻辑需求：version_id/current_revision_id/status/target_version_sut_ids/closed_at/closed_by_run_id）
 ├─< RequirementVersion（不可变快照：标题/描述/验收标准/优先级/约束/附件/目标 version_sut/页面/API）
 ├─< RequirementRelation（depends_on/conflicts_with/overlaps/shares_scope）
 ├─< RequirementClosure（检查项/证据/closed_by_run/at/reopen_count）
 └─< Conversation/Message（澄清来源）

Conversation（kind=clarification/design，version_id，title，status）
 ├─< Message（role/parts/refs/token）
 └─< Summary（滚动摘要 + 钉住事实 + 版本）
```

- 状态：`draft → clarified → closed`；任何一步可 `obsolete`；已澄清/已闭环可回退 draft。
- 确认 = 冻结 `RequirementVersion` 并写 `requirement_confirmation`（scope=requirement/batch、batch_id、actor/time、affected 集合）；单条/批量同一套记录。
- 编辑/AI 回退 = 新 draft 版本，旧版本保留；关联用例 stale。
- 闭环 = Agent `requirement.goal_met` + 服务端校验；不是用户按钮。校验项：目标 SUT/页面/API 覆盖、当前 confirmed 用例均有终态结果、`failed(application)` 已关联问题单、无开放设计/执行任务。
- **闭环允许失败/未修问题**：失败转问题单是产出，不阻止需求闭环；但每个失败应用用例必须有 issue 引用（或显式 waived 记录），否则闭环校验失败。
- 需求版本变化会使设计覆盖失效；设计团队自动补/重设计，闭环失效并重开。

### 6.3 设计与用例

```text
RequirementVersion
 └─< Scenario（场景：名称/描述/version_sut_ids/页面/API/排序）
      └─< Case（逻辑用例：current_version_id/status）
           ├─< CaseVersion（不可变：标题/描述/前置/测试数据/优先级/default_version_sut_id/页面/API/步骤/断言/来源）
           │    ├─< CaseStep（seq/action/expected/target_version_sut_id/page/API/account_role/data）
           │    └─< CaseAssertion（step_seq/kind/target/expected/target_version_sut_id/db_id）
           └─< CaseConfirmation（confirmed_by/at/scope/target/batch）
```

- 三级：需求 → 场景 → 用例；用例卡可带多个 SUT，以及页面/API 目标。
- 用例版本状态：
  - `draft → confirmed`（用户确认，冻结内容）；
  - `confirmed → stale`（需求版本变化/失效，退出可执行集，等待重设计）；
  - `confirmed → superseded`（**新版本被确认后**，旧 confirmed 才转历史；新 draft 生成时不改旧状态）。
- 逻辑用例的 `current_version_id` 指向最新版本；执行查询必须满足 `case_entity.current_version_id = case_version.id AND case_version.status='confirmed'`。
- 确认粒度：单用例 / 场景下全部 / 需求下全部；批量确认记录 scope、目标集合与 actor/time。
- 旧 confirmed/stale/superseded 版本保留用于历史报告；删除只做逻辑删除，不做物理覆盖。
- 跨系统用例：`CaseStep.target_version_sut_id` 可不同（空=继承 `case_version.default_version_sut_id`）；执行器为一个 case_run 管理多个 SUT 上下文。
- 设计团队内部自审不构成人工关卡；取消整版冻结/设计评审页签。
- **设计激活作用域**：`design_scope(version_id, requirement_id)` 记录哪些需求已进入设计；首次设计对话激活；需求版本变化按 `requirement_id` 继承激活，不依赖具体 version_id；新需求需新设计对话（或版本自动开关）才加入 scope。

### 6.4 执行与证据

```text
ExecutionIntent（case_version_id/version_sut_id/environment/code_baseline/purpose/status）
 └─< RunWave（同版本单活跃波：planned/running/done/failed/cancelled）
      └─< CaseRun（wave_id/intent_id/case_version_id/status/attempts/failure_class）
           ├─< CaseRunSUTContext（version_sut_id/browser_context/session/account_role/account_id）
           ├─< StepResult（seq/target_version_sut_id/status/observed）
           ├─< AssertionResult（kind/verdict/reason）
           └─< EvidenceRef

Evidence（不可变：kind/sha256/size/relative_path/version_sut_id/created_at）
Issue（open/confirmed/dismissed/merged；dedupe_key/reopen_count/merged_into）
Report（wave 报告 + version 总结；revision/status/as_of/HTML/PDF）
KnowledgeItem（global/system scope；draft/approved/rejected/revoked）
```

- 四态结果：`passed/failed/blocked/inconclusive`；`interrupted/skipped` 为过程态。
- **ExecutionIntent 是自动执行的驱动实体**：confirmed current case version + 环境/代码基线 + purpose 生成；open/claimed/running 状态唯一；case_run 完成后 intent=done；人工重跑/复测生成新 intent。
- 一个波次可包含不同 SUT 的用例；跨系统用例作为一个整体调度。
- 证据不可变；报告中记录 as-of 快照。
- 问题去重键跨波次稳定；重开/合并/驳回均留历史。

### 6.5 状态机

| 对象 | 状态 |
|---|---|
| Version（投影） | `initial / running / completed`；`completed` 由收尾 Agent 关闭，新目标自动回 running |
| Requirement | `draft / clarified / closed / obsolete` |
| CaseVersion | `draft / confirmed / stale / superseded`；stale=需求失效，superseded=新版本已确认后的历史版本；执行只认 current+confirmed |
| WorkItem | `open / claimed / running / waiting_human / done / failed / dead` |
| RunWave | `planned / running / paused / done / failed / cancelled`；paused 持久标记，恢复回 running |
| CaseRun | `pending / running / passed / failed / blocked / inconclusive / skipped / interrupted` |
| Issue | `open / confirmed / dismissed / merged`；confirmed/dismissed 可重开回 open |
| Report | `draft / published / stale`（revision 不可变） |
| KnowledgeItem | `draft / approved / rejected / revoked` |
| AgentRun | `running / done / failed / aborted / interrupted`；`context_compacted` 是过程事件，不是失败终态 |

---

## 7. Agent 体系

### 7.1 团队与角色

| 团队 | 角色 | 类型 | 职责 |
|---|---|---|---|
| 澄清 | Clarification Host | 对话 Host | 人机澄清；解释、提问、复述；不确认需求 |
| | Requirement Curator | worker | 抽取/改写/合并需求；产 draft 版本；关系建议 |
| | Context Scout | worker | 读附件/页面清单/代码仓/知识；产上下文摘要 |
| 设计 | Design Host | 对话 Host | 解释/修改脑图；不确认用例 |
| | Design Planner | planner | 扫描待设计/变化/stale；定本轮范围 |
| | Case Author | worker | 需求→场景→用例；步骤/断言/SUT/页面 |
| | Design Critic | worker | 内部自审；不构成人工关卡 |
| | Data Planner | worker | 前置/测试数据建议 |
| 执行 | Execution Planner | 确定性 runtime | 组波、并发、SUT 资源配额 |
| | Case Executor | worker | 单用例执行、多 SUT 上下文、取证 |
| | Failure Analyst | worker | 失败分类/问题候选 |
| | Coverage Analyst | worker | 覆盖缺口→设计任务 |
| 交付 | Issue Triager | worker | 问题分诊/重开/合并/去重 |
| | Report Writer | worker + 确定性渲染 | 波次/版本报告、HTML/PDF |
| | Knowledge Curator | worker | 跨版本知识沉淀/晋升/复审 |
| | Version Steward | worker | 收尾提议；重开检测；不直接改状态 |
| 运行时 | — | 非 Agent | 任务板、reconciler、租约、上下文、事件、LLM 网关、执行器 |

### 7.2 Agent run 模型

- `agent_run` 绑定 `work_item_id`（聊天为 `chat.turn`）、`context_packet_id`、`owner_instance`、`lease_epoch`。
- run scope 由运行时注入：`version_id / requirement_id / requirement_version_id / case_id / case_version_id / wave_id / version_sut_ids / page_ids / api_ids`。工具 schema **不再要求模型传 versionId**；模型只描述业务参数。
- 每次 run 重建上下文（任务包 + 会话窗口/摘要 + 团队频道摘要 + 工具/证据引用），不 `ListBySession` 全量回放。
- run 结束写结构化产物 + 事件 + 团队频道摘要；不把 run 全文作为下一次 run 的上下文。

### 7.3 工具与结构化提交

- 工具分域：需求、设计、执行、交付、资源/代码、知识。
- 所有写操作是"候选提交"：`requirement.draft_create/draft_update`、`requirement.relation_propose`、`requirement.goal_met`、`case.graph_propose`、`case.confirm`（仅服务端/用户）、`issue.propose`、`report.narrative_propose`、`version.close_propose`、`knowledge.propose`。
- 服务端校验：作用域、版本、引用存在性、状态迁移、证据 sha、去重键、代码锚点、权限。
- 高风险/不可逆动作（删除需求/用例、关闭版本、合并问题）由服务端或用户确认，不由 AI 直接执行。

### 7.4 预算、失败与恢复

- 每 run：max turns、软超时、硬超时、工具调用预算、token 预算。
- 失败分类：model error / context compacted / tool failure / budget / aborted / interrupted。
- 任务失败重试有上限；超限进 `dead`，在团队频道/待办中暴露，不无限重试。
- 实例崩溃：租约过期回收；只恢复过期租约，不因新实例启动把所有 running 标 interrupted（修正 0.1 D67 缺陷）。

---

## 8. 核心流程

### 8.1 管理员配置

1. 登录管理员 → 创建用户。
2. 配置 SUT：名称/简介/环境入口；维护页面清单（可导入 URL/路由）；AI 可提议页面，确认后入库。
3. 配置目标数据库（PG 只读）、测试账号（账号/密码/角色/环境）。
4. 配置 LLM/模型/Agent profile/执行限额/路径/保留期（系统设置中心）。
5. 可选：配置代码仓（用户也可自行新增）。

### 8.2 版本创建

1. 任意登录用户新增版本；选择多个 SUT，每个 SUT 选环境入口、页面范围、目标库、测试账号、代码仓。
2. 版本进入 `initial`；生成版本上下文包；等待澄清对话。
3. 版本可随时新增/调整绑定；已执行用例保留原绑定快照。

### 8.3 需求澄清

1. 用户点"新增对话" → 创建 `Conversation(kind=clarification)` + `chat.turn` 任务。
2. Clarification Host 读取版本上下文包（SUT/页面/代码仓/历史需求/知识），开场。
3. 用户输入/上传文件；附件解析为文本/图片引用；Host 与 Curator 创建/更新 draft 需求版本。
4. AI 提问、复述、关系建议；用户编辑/确认单条或批量"已澄清"。
5. AI 发现冲突可提议回退已澄清需求；用户确认后生成新 draft，关联用例 stale。
6. 设计团队只消费 `clarified` 需求；未澄清需求不进入设计。

### 8.4 用例设计

1. 用户到用例设计页，点"新建对话" → 创建 `Conversation(kind=design)` + 设计轮次任务。
2. Design Planner 扫描所有待设计需求（未设计、需求版本变化、用例 stale、执行缺口），按 `design_scope` 判断哪些已激活；给出本轮范围说明。
3. Case Author 生成场景/用例草案；实时渲染到脑图（图增量事件）；Critic 自审；Data Planner 补前置/数据。
4. 用户在对话里继续修改；AI 可新增/修改 draft 用例，不直接确认。
5. 用户按用例/场景/需求确认；确认后用例版本进入可执行集。
6. 首轮设计后，设计团队保持对版本覆盖的职责：执行缺口/需求变化自动补 draft，用户确认即可。
7. **D25（A+）**：默认新增需求批次需用户开新设计对话激活；当前设计对话活跃时，新澄清需求自动并入本轮。版本级开关 `auto_design_new_requirements`（默认关）开启后，新需求自动开设计轮次并自动创建一条设计对话供用户进入/纠偏。

### 8.5 执行

1. Reconciler 为“current+confirmed 用例版本 + 环境 + 代码基线 + purpose”生成 `ExecutionIntent`；同一 `(case_version_id, environment_fingerprint, code_baseline, purpose)` 只允许一个 open/claimed/running intent。
2. Execution Planner 发现无活跃波次 → 把可执行 intent 组波；同版本单活跃波；跨系统用例整体调度。
3. Case Executor 领取 case_run（带 intent_id）；为每个目标 SUT 建上下文/账号/Session；按步骤路由；写步骤/断言/证据。
4. intent 完成 → done；波次结束 → Failure Analyst 分类失败；Coverage Analyst 找缺口；Report Writer 出波次报告；Issue Triager 建/并问题。
5. 新确认用例产生新 intent，进入下一波；执行缺口自动产设计任务；用户确认新用例后产生新的 intent 再执行。
6. 人工重跑/复测 = 新建 intent（purpose=manual/retest）；旧 intent 保留历史，不覆盖。
7. 登录 2FA/验证码等无法自动完成 → `human_request(login_takeover)`；波次等待，用户完成后恢复；超时按策略跳过/失败并记录。
8. **退出执行集**：case_version 变为 stale/superseded 或不再是 current 时，取消其 open/claimed/running intent，并终止在飞 case_run（记录 `superseded`）；不再生成新 intent。需求回退/用例改版走同一规则。

### 8.6 交付

1. 问题：AI 只对 `failed(application)` 建候选；服务端按 dedupe key 合并/插入；人工确认/驳回；支持重开/合并。
2. 报告：波次报告 + 版本总结；`report_revision` append-only；HTML 为权威产物，PDF 由独立渲染 worker 生成；revision/as-of/stale。
3. 导出：用例/报告/链路导出生成 `export` 逻辑对象 + `content_ref`；GC 以 content_ref 账本判定引用；下载走鉴权端点。
4. 知识：版本来源知识经审核晋升全局；澄清/设计按预算注入；支持撤回/复审。

### 8.7 版本收尾与重开

1. 版本状态迁移：`initial → running`（首个需求/对话/设计/确认）；`running → completed`（收尾谓词）；`completed → running`（新需求/需求重开/新用例确认/新执行 intent/人工重开）。
2. Version Steward 只对 `running` 版本做收尾评估：无 open/claimed/running/waiting_human work item、无 open/claimed/running ExecutionIntent、无 open human_request、无活跃/暂停波次/在飞 case_run、所有需求 `closed/obsolete`、**版本总结报告**已发布。`completed` 版本只扫描重开信号，不重复收尾。
3. 通过结构化 `version.close_propose` 提议；服务端校验后置 `completed`（记录 `closed_at/close_reason/close_run_id`）。
4. 新增需求、确认新用例、需求重开等产生新目标 → 完成标记失效，自动回 `running`。
5. 版本状态只影响 UI/报告归档；不影响任何功能可用性。

### 8.8 探索链路

- 一级 Tab；读模型聚合：需求版本 → 场景/用例版本 → 波次/执行 → 步骤/断言/证据 → 问题 → 报告 → 需求闭环。
- 节点/边带 SUT/页面/波次/状态；支持过滤、深链、全屏、历史回放、实时跟随。
- 实时更新通过 SSE；历史/回放通过事件游标 + 读模型快照；节点预算/截断/重连沿用 0.1 P8 经验。

---

## 9. 上下文工程

### 9.1 分层上下文

| 层 | 内容 | 预算/生命周期 |
|---|---|---|
| 版本上下文包 | SUT/页面/需求版本摘要/用例覆盖/闭环状态/未决问题/最近结果/知识引用 | 版本级，增量刷新 |
| 任务上下文包 | 任务目标、输入产物、约束、验收、相关代码/用例、历史尝试 | 每 work item 重建 |
| 会话记忆 | 最近窗口 + 滚动摘要 + 钉住事实/决定 | 线程级；AI 摘要 + 结构化钉住 |
| 团队频道摘要 | 本团队最近结构化帖子 + 与本任务相关帖子 | 版本×团队 |
| 工具/证据上下文 | 工具结果限额、超限落盘、按引用分页 | 单 run |

### 9.2 规则

1. 任务目标/约束/当前用例进 **system**，不参与压缩；态势/概览进 user，可降级。
2. 预算按 **token** 计算：模型窗口 − 输出预留 − 工具/证据预留；每层有上限。
3. 超阈值触发滚动摘要：保留钉住事实 + 最近窗口，旧内容压成结构化摘要；摘要失败回退确定性裁剪。
4. 工具结果超限落盘，只回 head + 引用；执行按用例续跑、按步骤检查点恢复。
5. `context_overflow` 不再判 run 失败：先压缩重试；仍不可行则拆任务/子会话/生成人工请求。
6. 每次 run 持久化 `context_packet`（hash/各段 token/引用），用于复现与审计。
7. 新会话不读历史全文，只读版本包 + 历史线程摘要 + 相关产物。

---

## 10. 配置架构（.env 最小化）

### 10.1 启动配置（仅 4 项）

| 变量 | 用途 |
|---|---|
| `EZTEST_DATABASE_URL` | PostgreSQL 连接 |
| `EZTEST_API_PORT` | API 端口 |
| `EZTEST_WEB_PORT` | Web 端口 |
| `EZTEST_EXECUTOR_PORT` | 执行器端口；默认 URL 由 `127.0.0.1:{port}` 派生，完整 URL 在系统设置中心可改 |

**没有第 5 个启动 env**：执行器 host/URL、数据目录（默认 `./.data`）、所有路径/限额/密钥都进 DB 设置中心。前端/后端/执行器端口只用于启动绑定。

### 10.2 系统设置中心（DB）

`system_setting(key, value_json, category, type, is_secret, restart_required, updated_by/at, version)`。

| 类别 | 内容 |
|---|---|
| LLM | 供应商/协议/baseURL/Key/模型目录/默认模型/超时/重试/多模态 |
| Agent | profile prompt/工具白名单/模型/预算/并发 |
| 执行 | 波次并发/每 SUT 并发/工具预算/超时/证据限额/浏览器白名单 |
| 路径 | 证据/附件/导出/报告目录（默认 `./.data/...`） |
| 保留/GC | 各对象保留期、dry-run 默认、引用保护 |
| 报告 | 模板/章节开关/HTML/PDF 渲染开关 |
| 知识 | 审核人/scope/注入预算 |
| 安全/审计 | 限流、审计保留、UI 脱敏 |
| 功能开关 | 实验特性/自动补用例/自动设计观察 |

### 10.3 启动顺序

`读 env → 连 DB → 迁移 → 加载 settings（缺失则种子默认）→ 构建依赖 → 启动 HTTP/worker。`

- 大多数设置热生效（读取时缓存 + 版本失效）；端口/DB/执行器 URL 等标记 `restart_required`。
- 设置变更审计；secret 在 UI 只显示掩码。

---

## 11. 多 SUT 与跨系统执行

### 11.1 模型

**统一命名**：资源层用 `sut_id`；执行层一律用 `version_sut_id`（版本内 SUT 绑定）。页面/API/账号/DB 必须校验属于同一个 `version_sut` 的 SUT/环境。

- `VersionSUT` 是版本内 SUT 的绑定；用例/场景/步骤引用 `version_sut_id` + 页面/API。
- `case_version.default_version_sut_id` 是默认目标；一条用例可有多个步骤，每步 `target_version_sut_id` 可空，空则继承默认。
- 一个 case_run 可创建多个 SUT 上下文，按 `(version_sut_id, account_role)` 组织；同一 SUT 可同时有多个角色账号，每个上下文独立浏览器/Session。
- `case_run_sut_context` 记录 `version_sut_id/account_role/account_id/browser_context_id/lease_epoch`；账号按角色解析，不把具体账号写死在用例里。
- 证据、步骤结果、断言、失败分类、问题引用、报告计数都带 `version_sut_id`；`sut_id` 只用于资源展示/聚合。

### 11.2 调度

- 同版本单活跃波；波内并发受全局/每 `version_sut` 配额限制。默认：全局 3、每 `version_sut` 2、跨系统用例 1（可配）。
- 跨系统用例整体调度：一次性预留所需 `version_sut` 配额，避免 A 等 B 的死锁；跨系统用例默认串行/低并发。
- 单 SUT 用例可并行；跨系统用例按依赖顺序执行步骤。
- 执行器上下文有 TTL；波次结束/失败先 cleanup 再释放配额；实例崩溃按 lease/epoch 回收。
- 波次报告按 `version_sut` 分组统计（同时给 SUT 名称展示）；版本总结按需求闭环统计。

### 11.3 账号与数据

- 测试账号按 `version_sut`/角色解析；用例步骤只指定 `account_role`，执行时从版本绑定中解析具体账号；同 SUT 多角色可并存。
- 目标库按 `version_sut` 绑定；断言可指定 DB；跨系统用例可分别查询。
- 数据准备/清理按 `version_sut` 分组；跨系统数据一致性由用例前置/后置步骤表达。

---

## 12. P9 设计

### 12.1 D59 报告 HTML/PDF + 模板

- HTML 为权威报告产物：服务端确定性模板渲染，快照冻结；`report` 指向 `current_revision_id`。`report_revision` 的**内容字段 append-only**；`render_status/pdf_ref` 是可更新的渲染元数据，不改变内容/as-of。
- PDF 由独立 headless 渲染 worker 从冻结 HTML 生成；`report.render_pdf` 任务可重试，不阻塞 HTML 发布；失败后 reconciler 补任务。
- 内置模板 + 章节开关 + `template_version`；0.2 不做用户自定义模板编辑器。
- 波次报告 + 版本总结；revision/as-of/stale；旧 revision 不覆盖；render_status 记录 HTML/PDF 状态。
- 证据以缩略图/链接/计数呈现，不把大图嵌入 HTML（可后续扩展）。

### 12.2 D63 跨版本知识

- 知识是**跨版本**的：`scope=global`（全库）或 `scope=system`（同一 SUT 族）；版本只是来源，不拥有知识。
- 版本来源知识显式"晋升"；记录 `promoted_from_version_id`；管理员审核；支持撤回/复审；`approved → revoked` 后不再注入。
- 澄清/设计注入按预算、标签/关键词检索；带引用；PostgreSQL 全文，不做向量 RAG。
- 冲突/过时/删除源版本的处理：保留来源引用，知识标记 stale/revoked，注入时排除。

### 12.3 D65 GC

- 统一内容账本：附件/证据/导出/报告 HTML/PDF 都登记到 `content_ref`；`export`、`report_revision`、`evidence`、`attachment` 只做语义关联，不另建真值。
- GC 默认 dry-run；引用保护（版本/需求/用例/问题/报告/知识）；宽限期 24h；审计；删除留 tombstone。
- 默认保留期：临时文件 24h；被替代导出 7d；无引用内容 30d；可配置。
- 只清无引用/临时/被替代导出；旧报告 revision 与有引用附件不自动删。
- 跨实例需共享存储；清理任务带 lease/fencing。

### 12.4 D67 多副本

- 任务板 lease/fencing；实例启动只恢复过期租约，不批量中断 running。
- 执行器注册/心跳/容量/排空；case_run 带 owner_executor_id/dispatch_epoch/lease_until/dispatch_token，结果写入校验 epoch/token，拒绝迟到写回。心跳超时→offline；其 case_run 置 interrupted(executor_lost)、epoch++、按 intent 重派。
- SSE 跨实例：PG LISTEN/NOTIFY 或事件轮询；快照游标/回放不变。
- 共享文件存储（NFS 或等价物）；readiness/排空/滚动升级。
- 限流按实例或 DB 额度；文档明确总速率。
- 目标：多副本安全 + 基础编排；不承诺自动扩缩容/多区域 HA。

### 12.5 问题重开/合并/跨波次去重

- 状态：`open/confirmed/dismissed/merged`；confirmed/dismissed 可重开回 open（原因/次数/历史）。
- 合并：源问题 `merged` + `merged_into_id`；refs/证据/报告引用迁移或别名；保留历史。
- 去重：确定性 dedupe key 跨波次稳定 + AI 语义合并建议（人工确认）；不做无审核自动合并。
- 报告：旧 revision as-of 不可变；新报告反映当前状态；diff/fingerprint 按快照计算。

---

## 13. 多实例、部署与运维

- 单机默认；多副本安全为 0.2 目标，完整编排为 P9 波次。
- 迁移只追加、启动 advisory lock、checksum；备份恢复沿用 0.1 经验。
- 执行器独立进程；API 不装 Chromium；执行器可水平扩展但需注册/健康。
- 健康：API/DB/LLM/executor 分别探测；`/api/health` 不冒充全链路可用。
- 观察：事件/审计/团队频道/任务板/链路；SLO 与告警后置/按需。
- 安全：受控开发测试网络；凭据 AI 可读但 UI 掩码；不做生产级加固。

---

## 14. 信任模型与安全

- **定位**：开发/测试/验证环境，目标是尽可能发现问题；不是生产系统。
- **凭据**：测试账号密码允许 AI 读取使用；LLM Key/账号密码存 DB；UI 掩码；访问审计；不做密钥托管/信封加密。
- **权限**：单管理员 + 普通用户；管理员配置资源/设置/知识审核；普通用户可创建代码仓/版本并执行工作流。
- **边界**：不接外部工单/CI/IM；不暴露公网；CSP/HTTPS 由部署方；不做多租户。
- **审计**：关键写操作、凭据访问、任务认领/重试、版本收尾/重开、问题合并/重开。

---

## 15. 前端架构与信息架构

- 全局壳：版本 / 问题 / 报告 / 知识 / 资源 / 质量 / Agent 中心 / 设置。
- 版本详情 8 Tab：总览 / 需求与澄清 / 用例设计 / 执行 / 探索链路 / 问题 / 报告 / 代码与变更；知识只在全局。
- 需求/设计：对话主区；Markdown；附件上传（doc/pdf/md/ppt）；历史会话；内联确认卡；SUT/页面/API 标签。
- 脑图：需求→场景→用例横向布局；用例卡 SUT/页面/API；跨系统徽标；右侧详情；确认按钮。
- 设计待办：存在“已澄清未设计”需求且未开自动设计时，展示“N 条待设计”CTA；版本设置提供 `auto_design_new_requirements` 开关（默认关）。
- 执行：波次 + 实时 + 结果/证据 + 链路入口；多 SUT 分组。
- 链路：一级 Tab；动态图；SUT/页面/波次过滤；问题侧栏；历史回放。
- 复用 0.1 的 SSE 客户端/Transcript/Composer/SessionList 思路；重写澄清聊天、脑图布局、链路图与新数据模型适配。
- 参考 [临时 UI 草图](ui/mock.html)。

---

## 16. 质量与验收

- **旅程验收**：管理员配置 → 用户建版本/绑多 SUT → 澄清 → 设计 → 确认 → 执行 → 问题/报告 → 闭环 → 重开。
- **自主性验收**：用户只做三个半自主动作；设计/执行/交付无人工触发；任务板可恢复。
- **多 SUT 验收**：跨系统用例执行、分 SUT 证据、分 SUT 报告。
- **上下文验收**：长会话/长执行不因 context_overflow 失败；摘要/压缩/落盘可查。
- **P9 验收**：HTML/PDF、跨版本知识、GC dry-run、多副本安全、问题重开/合并/去重。
- **回归**：0.1 P0–P8 能力逐项有 0.2 验收路径；Fake LLM + 真实 LLM 双轨。
- **质量指标**：需求闭环率、用例确认率、执行通过率、问题有效率、证据完整率、上下文压缩次数、任务失败/重试率。

---

## 17. 演进与迁移

- 0.1 数据不做原地迁移；0.2 新 schema；旧库只读归档/导出（待最终确认）。
- 0.2 内部按能力波次推进：
  1. 平台底座 + 配置中心 + 多 SUT 资源
  2. 团队运行时 + 任务板 + 上下文
  3. 需求/澄清
  4. 设计/用例/确认
  5. 执行/证据/波次
  6. 交付/问题/报告/链路
  7. 代码智能/视觉断言/质量/搜索/待办/Agent 中心（全功能对齐）
  8. P9 五项
  9. 硬化/验收
- 多实例安全原语从第 1 波就按多副本设计；P9 的部署编排后置。
- 0.1 的确定性底座（身份/LLM/事件/存储/审计/资源/执行器协议/报告渲染/fake LLM）优先移植，不重复踩坑。

---

## 18. 风险与参数默认

| 风险 | 影响 | 对策 |
|---|---|---|
| 多 SUT 跨系统执行复杂度 | 执行器/调度/证据/报告都变复杂 | 步骤级 SUT、整用例调度、预留配额、先做单 SUT + 跨系统最小闭环 |
| 全功能对齐 + P9 范围过大 | 0.2 周期/质量风险 | 能力波次 + 每波验收；共享任务板/上下文先跑通 |
| AI 自动补用例导致用例膨胀 | 确认负担/成本 | draft 必须用户确认；覆盖缺口有预算/去重；团队频道摘要 |
| 上下文摘要丢关键信息 | 复述/决策丢失 | 钉住事实 + 结构化摘要 + 可展开原文 + 评测 |
| 设计对话触发与自动补的边界 | 用户心智/重复设计 | D25 A+：默认新需求需新对话；变化/缺口自动补；版本级自动开关；UI 明确提示 |
| 频道噪音/上下文成本 | Agent 质量下降 | 结构化帖子 + 摘要 + 相关检索；不实时唤醒 |
| 多副本文件存储 | 证据/报告不可用 | 共享存储要求；单机默认；P9 编排 |
| 配置中心迁移 | 0.1 用户不习惯 | 4 项 env + 默认值 + 管理 UI + 文档 |
| 安全信任模型 | 凭据泄露 | 受控网络 + 审计 + 掩码；明确非生产定位 |

**参数默认（已定，2026-10-31）**（详细说明见 [open-decisions.md](research/open-decisions.md)）：
1. 用例导出：Markdown / Mermaid / OPML / CSV；Excel 后置。
2. 报告：HTML 权威 + 独立 headless 渲染 worker 出 PDF；1 套内置模板 + 章节开关；不做自定义模板编辑器。
3. 知识：`global` + `system` scope；显式晋升；管理员审核；标签 + PG 全文；不做向量 RAG。
4. GC：统一 content ledger；dry-run 默认；24h 宽限；临时 24h / 被替代导出 7d / 无引用 30d；旧 report revision 不自动删。
5. 0.1 数据：新库；不原地迁移；旧库只读归档/导出。
6. 账号：用例只指定 `account_role`；执行按 `version_sut + role` 解析；同 SUT 多角色可并存。
7. 页面/API：同时存完整 URL 与 route_template / method+path_template；管理员确认 AI 提议；去重按 template。
8. 多 SUT 并发：全局 + 每 `version_sut` 配额；跨系统用例默认低并发（1）；上下文 TTL + 波次结束清理。
9. 团队频道摘要：每 20 条或 10 分钟；保留最近 N 版；帖子全量审计；不做自由群聊控制面。
10. 需求字段：必填 title/description/acceptance/priority；推荐目标 version_sut/页面/API、业务规则、前置、异常、数据约束、依赖。
11. 报告种类：每波次 `report.wave` + 版本 `report.version`；版本总结在收尾前生成；均可 HTML/PDF；旧 revision 不覆盖。

---

*文档版本：v0.3（讨论稿，2026-10-31，已并入独立评审 P0/P1 修订与全部参数默认）。下一步：基于本文与概要设计进入 Superpowers spec/plan。*
