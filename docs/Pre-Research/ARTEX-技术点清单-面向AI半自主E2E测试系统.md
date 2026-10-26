# ARTEX 技术点清单 —— 面向《AI 半自主端到端测试系统》的借鉴地图

> **研究对象**：`D:\Project_new\ARTEX`（AI 自主渗透测试系统，AGPL-3.0）
> **仓库基线**：HEAD `4379d69`（0.3.15 之后），Go 单体后端（`net/http` + PostgreSQL/pgx + `norma` agent SDK v0.4.3）+ Next.js 16 静态导出 `go:embed` 单二进制；规模 327 个 `.go`（含 153 个测试文件）≈ 8.7 万行、187 个 `.ts/.tsx` ≈ 5.3 万行、`db/schema.sql` 1322 行。核心机制在 `README.md:273-440`，但**大量设计只存在于代码注释里**（`docs/` 仅剩一篇文档）。
> **研究方法**：静态源码阅读（当前环境无 Go/Node 工具链，未运行测试、未启动服务）；5 个方向并行深读（agent 层 / 引擎与服务层 / 数据·证据·报告 / 前端 / 支撑子系统）+ 交叉复核。
> **用途**：为 EzTest（AI 半自主端到端测试系统）选择可迁移的架构、机制与交互设计。
> **合规提醒**：ARTEX 为 AGPL-3.0 且 README 声明仅限学习研究。**借鉴设计思路没问题；直接复制源码会触发 AGPL 衍生作品义务（含网络服务开源）**，新系统应"重写实现、复用模式"。

---

## 目录

- [一、先给 8 条总判断（比技术点更重要）](#一先给-8-条总判断比技术点更重要)
- [二、技术点清单（24 项）](#二技术点清单24-项)
- [三、ARTEX → E2E 概念映射表](#三artex--e2e-概念映射表)
- [四、如果现在设计这套系统：骨架草案](#四如果现在设计这套系统骨架草案)
- [五、ARTEX 的空白（必须新设计）](#五artex-的空白必须新设计)
- [六、照抄会踩的坑 Top 12](#六照抄会踩的坑-top-12)
- [七、借鉴优先级与下一步](#七借鉴优先级与下一步)
- [八、后续深入提问索引（对应源码位置）](#八后续深入提问索引对应源码位置)
- [附录 A：ARTEX 怎么做到端到端执行？——以"给账号密码自动登录"为例](#附录-aartex-怎么做到端到端执行以给账号密码自动登录为例)

---

## 一、先给 8 条总判断（比技术点更重要）

1. **ARTEX 的"自主"来自骨架，不来自 prompt**：外置状态（双图）+ 事件（谁变了）+ 无状态会话（每轮重建）+ 外置记忆（transcript / activity / todolist / digest）+ 预算收尾。照搬骨架比调提示词收益大一个数量级。
2. **双图分离**是最核心的数据设计：全局"被测对象真值图"（资产图）与每任务"执行过程图"（探索图）解耦，锚点相连 → 可追溯、可算覆盖度。
3. **半自主 = 可观察 + 可干预 + 可收尾**：每步 activity 留痕、pause/steer/kill/hint/直投/审批、预算耗尽先写回再结束。审批弹窗只是其中一环。
4. **职责分离**：goals（需求拆解）/ planner（规划）/ worker（执行）/ mainagent（人机接口）/ reporter（报告）/ retester（复测）/ auto（平台操作）各一个专职 agent，**报告绝不由执行 agent 顺手写**。
5. **让 AI 的重复/碎片写入自动收敛**：自然键 + 部分唯一索引 + `ON CONFLICT` 合并；节点/父子/去重全部程序算，agent 只交"我看到了什么"。
6. **证据不可变、报告可过期**：内容寻址证据 + `evidence_version`/`report_evidence_version` 双版本戳 + CAS 写 + stale 展示，绝不静默覆盖。
7. **能力扩展靠技能体系**：`SKILL.md` 渐进披露 + MCP 延迟解锁 + found/miss 使用账本；`playwright-cli`、`api-recon` 两个技能可直接改造成 E2E 执行能力。
8. **前端把长时 agent 映射成"一条带 seq 的事件流"**：单任务单条 SSE + snapshotCursor 衔接历史/实时 + seq 即身份 + 人环卡片内联，这套 UI 协议可整段搬。

---

## 二、技术点清单（24 项，分 5 组）

### A. 运行时与任务编排（引擎层）

**T1 事件驱动双循环引擎**
机制：每任务 1 个 `plannerLoop` + N 个 `workerLoop`；图变 → 800ms debounce 合并唤醒 → planner 派意图 → worker CAS 领取 → 写回图 → 再唤醒；planner 心跳（下限 600s）兼作死锁兜底与在跑 work 监督；**"全图无 open/running 意图"时强制本轮必须产意图（停摆自救）**；每轮规划打 `kind='round'` 边界事件供 UI 切分。
`server/engine.go:800-914`、`agent/planner.go:302-305,452-459`、`server/engine.go:871-874`
→ 迁移：测试系统的"用例设计轮"可完全复用；触发源 = 用例完成/缺陷登记/需求或约束变更/人工直投 + 低频心跳。**空队列必须触发规划，不能安静结束**。

**T2 意图/用例生命周期状态机 + CAS 领取**
机制：`open → running → done | blocked | exhausted | stopped | paused | deleted`；领取 = `UPDATE ... WHERE state='open'` 单行 CAS（不是先查后写）；暂停 = 置 paused **并取消在跑 run**（否则长任务停不住），恢复时意图回 `open` 靠 transcript 接着跑；终态由 `runIntent` 一大段 if-else 分流（err→blocked、撞步数/超时→exhausted、终态取消→stopped、暂停→回 open）；取消原因具名化（`cancelcause.go`，未挂 cause 显式记 `canceled_no_cause`）。
`server/engine.go:1047-1156,1237-1245`、`db/exploration.go:1172-1181`、`agent/cancelcause.go`
→ 迁移：测试结果不能只有"通过/失败"，至少 **通过 / 失败(有证据) / 阻塞(环境、依赖、LLM) / 未完成(预算)** 四态；模型/API 故障绝不能记成用例失败。这条状态机就是新系统的"执行语义规格"。

**T3 预算、收尾（settlement）与任务级总超时**
机制：worker 三件套 `MaxTurns` + `MaxDuration`（墙钟，轮边界判）+ `Settlement`（命中预算后注入收尾提示、隐藏 `Bash`、再给 10 轮把结果落库）；任务级 deadline 用独立协调器：settling → 停领新活/丢普通唤醒 → 等 in-flight drain（90s 宽限）→ 终局一轮 planner（只判定、不产新意图）→ 守卫落 done|timeout；per-run 与任务超时两套收尾词语义相反，按实际 reason 现场挑。
`agent/worker.go:144-154,424-470`、`agent/wrapup.go`、`server/engine_timeout.go:183-227`、`agent/taskclock.go`
→ 迁移：E2E 必须有总时长预算 + 优雅收尾；撞预算时"先写证据/半成品结论，再结束"，收尾阶段禁副作用工具、留记录工具。

**T4 任务准入、并发治理与生命周期安全**
机制：单入口 `admitTask`（创建/恢复/重跑/复活全走它）+ 持久 FIFO 队列 + 全局并发上限（默认关闭，开启默认 5）；启动恢复四件套（重置遗留 running 意图→恢复 paused→重启循环→补 deadline 协调器）；删除屏障三件套（deleteMu + inflight 计数 + runtime WaitGroup，先挡新写入→等归零→删数据→停运行时，失败按 DB 已提交行回滚）；锁序固定 `concMu→deleteMu→inflight`。
`server/goals.go:110-215`、`server/manager.go:1141-1229`、`server/server.go:264-294`、`server/engine.go:183-291`
→ 迁移：测试任务同样有"暂停/排队/超时/删除"竞争；**先停写入再改状态**，否则出现"状态 running 但 paused"或删除后又被写回。

**T5 按任务/按角色路由 LLM（主备链 + 额度换号）**
机制：三级优先级 = Agent 角色绑定 > 任务 profile 链（active 游标，revision CAS 推进）> 全局；额度耗尽（402/明确余额耗尽）标记 quota_exhausted 并换下一个；**流式调用以 `committed` 边界决定能否换号重放**（已向调用方交付输出就禁止重放，避免重复副作用）；每任务缓存 planner/worker/main/goals 四个 bundle。
`server/task_llm.go:110-147,287-384`、`server/task_resolution.go:44-101`、`db/task_context.go:455-545`
→ 迁移：测试系统常见"设计用强模型、执行判定用便宜模型"；每个角色绑模型 + 主备链 + 换号游标值得照搬，**并在结果里记录实际 model/profile（否则回归无法复现）**。

**T6 事件触发与自动编排（不是状态机，是"水位 + 专职 agent"）**
机制：`agent_triggers` 六类条件（interval / on_finding / on_goal_met / on_task_timeout / on_tool_call / on_task_create）；5s tick 的 Scheduler 用**持久水位**（last_finding_id 等）保证重启不重发、后启用触发器不回灌历史；每个 agent 可配串行/并行与 merge（reporter 特意 `parallel + merge=none + maxParallel=5`）；另有跨任务工具面（Auto agent 的 `spawn_task/pause_task/get_task_graph/...`）与通知投递引擎（租约 + 令牌桶 + 退避）。
`server/scheduler.go`、`server/conversations.go:610-660`、`server/orchestration.go:65-86,731-780`、`server/notifier.go`
→ 迁移：**"用例失败→自动复测""缺陷登记→自动写缺陷报告""任务完成→汇总通知/CI 回调"** 全部挂这套；坑：先持久入队再推水位（ARTEX 现版有内存队列崩溃丢事件的缺口）。

---

### B. 状态模型与上下文工程

**T7 双图 + 锚点 + 覆盖度**
资产图：6 类节点（root_domain/subdomain/ip/service/app/endpoint），全局共享、程序算父子与归属；探索图：`goal/intent/fact/finding/hint/digest` + 5 类边 `spawns/derived_from/yields/proves/covers`，任务创建时 seed 一个 `fact + state='origin'` 作根（保证"每个意图都能连到事实"）；`exploration_anchors(node_id, asset_id)` 双向关联且不 gate 可见性；覆盖度 = **scope 内资产中被 `kind='fact'` 节点锚定过的占比（finding 不算覆盖）**；覆盖图节点 key 规则（`a:<id>`/`c:<id>`/`r:<domain>`）。
`db/schema.sql:170-281`、`db/constants.go:4-40`、`db/task_scope.go:262-396`、`README.md:333-367`
→ 迁移：**资产图 → 被测应用模型**（页面/路由/API/组件/依赖/环境），**探索图 → 需求→用例→结果→缺陷的思维导图**；锚点 = 用例↔页面/接口/需求的可追溯关系表；覆盖度口径要重定义成"需求/用例维度"而不是资产维度。

**T8 目标、约束、范围三件套（需求侧的现成范式）**
机制：`goals` 一次性目标分解：只登记"最终可核验的结果"（明确禁止把侦察/验证/攻击步骤当目标），同时抽 `allow/deny` 操作约束（**必须自包含写死具体目标，不许"当前目标"这类指代**）与最小资产范围（禁止把 `a.example.com` 缩成 `example.com`）；约束渲染成 system 追加块，措辞"最高优先级、凌驾一切探索启发式、发现约束外新目标≠获得授权"；量化验收 `prove_goal` 前必须核对实测值（如覆盖度未达标禁止标记达成），最后一个目标 met 自动收官。
`agent/goals.go:19-67`、`agent/constraints.go:14-49`、`agent/planner.go:315-316,394-396`、`agent/tools.go:1184-1200`
→ 迁移：**这就是"需求澄清"的输出契约雏形**：目标（可验收结果）+ 约束（自包含）+ 范围（最小授权）+ 量化验收；澄清 agent 只写这三样进图，不跑测试。

**T9 规划器机制（对"用例设计"借鉴价值最高）**
机制：意图 = 开放方向（不是固定分类）+ **强制血缘**（`parent_ids` 只能挂已存在的 fact/finding，顶层自动挂 origin fact）；overview 预取（open 30/recent_done 12/recent_facts 20/finding 10/digest 15）+ **窗口与真实总数双写**（防止"没显示=没做过"）；重派按 state 分流（done 不原样重派；exhausted/blocked 先读 trace 再决定续跑/重派/换法；否定结论按 observed/inferred 判断，同一否定方向至多复核一次）；跨轮共享 todolist 承载串行链（登录→下单→支付），每轮只派"前置已满足"的下一步；0 意图是正常结果但有硬底线；规划器不执行（但工具集里其实有 Bash，边界只靠提示词——这是坑）。
`agent/planner.go:298-340,355-472`、`agent/tools.go:354-552,1028-1089`
→ 迁移：把「需求条目/代码现状事实」当 fact、「用例」当 intent，父子边即思维导图；**落库是图，导出是树**。规划器工具用白名单强制只读，别重蹈"提示词当边界"的坑。

**T10 上下文工程（长跑不崩的关键）**
机制：① **mandate 进 system / 态势进 user**——worker 的"当前意图 + 目标资产 + 启动指令"拼进 system（system 每 run 重拼、不会被 compaction 压掉，"意图丢失比省 token 严重"），全局 overview 留 user（可降级）；② 冷图压缩 cold-digest：hot/cold 判定（能达活意图的祖先 + 活意图 + 新鲜子节点）、连续冷 ≥6 轮且块 ≥2 才折叠、union-find 分组、digest 是派生节点 + `covers` 边（原节点不删）、generation + 签名复用、commit 前"复活复核"、`expand_digest` 逐级还原；③ 大工具输出 spill 到 `cmd-output/` 只留 head+指针，tool result 默认截断 3 万字符，traffic 大正文给 `@blob sha256:` 分段取；④ 压缩窗口 clamp [32K,1M]，实验压缩可开关+失败回退。
`agent/worker.go:384-421,463-465`、`agent/coldgraph.go`、`agent/compaction.go`、`agent/tools_digest.go`、`agent/noa.go`
→ 迁移：执行 agent 的「当前用例+前置条件+预期结果+环境参数」必须进 system；**历史用例/缺陷按血缘折叠成摘要、节点保留、可展开**——这是长测试会话最值得抄的上下文机制。坑：todolist 在内存、冷压缩异步需复活复核、digest 质量决定规划质量。

**T11 跨任务只读继承（跨迭代复用）**
机制：`task_relations` 直接、只读、不递归地继承源任务黑板摘要（goal/fact/finding/intent/hint/digest/scope/coverage），带 `inherited/source_task_id` 标记，分层预算（总 48K runes 按源数均分、单源上限 8K）；写操作只作用于本任务。
`db/exploration_sources.go`、`agent/tools.go:515-592,661-861`
→ 迁移：上一轮迭代的用例/缺陷/登录态/测试数据作为"只读继承"；**一定要带来源标记**，否则报告会把别人的结论算到自己头上。

---

### C. Agent 角色、提示词与能力扩展

**T12 角色拓扑与职责边界**
内置角色：`goals`（目标拆解）、`planner`（规划）、`worker`（执行）、`mainagent`（人机接口，只观察+操舵：hint/直投/steer/set_goals/set_constraints）、`auto`（平台操作助手：spawn_task/skill/tool/MCP 管理）、`pentest`（单 agent 从侦察到收尾）；另有 seed 的自定义 agent：`reporter`（report_finding 触发写报告）、`retester`（回归复测）。planner 是"唯一**自主**意图生成者"，人工直投是显式例外。
`agent/promptcatalog.go:109-121`、`agent/mainagent.go:84-101`、`server/orchestration.go:731-780`
→ 迁移：澄清 / 设计 / 执行 / 缺陷归类 / 报告 / 复测 / 助手，一一对应；**每个 agent 只做一件事，产出以结构化写入结束**。

**T13 提示词工程（可运营 + 护栏不可改）**
机制：三段式 = 可编辑正文（段 A，种入 `agent_prompts` 表、UI 可改、带版本、reseed 迁移）+ 条件尾（段 B，按运行期事实 gate，如流量工具开关决定是否注入说明）+ 代码固定尾（段 C，如"中间产物必须写本 run 工作目录"）；变量契约 `missingkey=error` + 渲染失败回退内置默认；提示段与工具装配同步 gate（说到的工具一定在）；收尾词显式声明"覆盖此前一切继续探索指令"。
`agent/prompt.go:38-63`、`agent/worker.go:250-294`、`server/orchestration.go:566-686`
→ 迁移：澄清清单/用例写作规范可运营配置；"不得臆造需求、必须复述确认、证据必须落盘、报告必须有证据"留在代码尾。

**T14 工具装配流水线与能力边界**
机制：`base`（领域 + host 工具）→ `AugmentTools`（skill 打包成一个 Skill 元工具 + MCP 展开成 `mcp__server__tool`）→ `ToolResolve`（DB tools 表按 agent 过滤 + 覆盖描述/schema + 注入缺省参数）→ `findingWorkflowTools`（按特性开关改契约）→ `guardPanic`（工具 panic 兜底，最外层）。不变量：**handler 永远来自代码，DB 只能改散文与默认值**；高风险工具（`goal_met`）进目录但默认不绑；运行期身份（task/intent/session）用 ctx 注入，**不接受模型传参**；worker 刻意不给 MultiEdit/Glob/Grep（收敛工具面）。
`agent/assembly.go:39-86`、`agent/toolcatalog.go:10-59`、`agent/deferred.go`、`agent/runinfo.go`
→ 迁移：E2E 工具（浏览器、HTTP、DB 断言、截图、seed/teardown）分「能力（代码）」与「描述（可运营）」；清库/重启环境等危险动作默认不绑。

**T15 技能体系 + MCP + 自定义工具（能力持续增厚）**
机制：`SKILL.md`（agentskills.io，YAML frontmatter：name/description 必填 + `mcps` + `allowed-tools`）→ 第一层只暴露技能名+描述，调用 `Skill(name)` 才返回正文；第二层 `mcps:` 指名服务器，其工具延迟暴露、加载技能后解锁；第三层正文只给"去哪找"，`references/*.md` 按需读、脚本复制到 OUTDIR 再改；会话恢复从历史重放 Skill 调用恢复解锁集；`skill_usage` 记录调用（含 `found=false` 的"想要但不存在的技能"→ 该写什么技能的路线图）。内置 `playwright-cli`（命令原语 + snapshot ref：`open/goto/snapshot/find/click e15/fill/route/storage/tracing`；`--raw` 供 jq/diff）与 `api-recon`（静态 harvest + 运行时 hook 两层、门禁 A/B、参数矩阵、完成定义 checklist、无凭据 stub 三层）——**这两个技能几乎就是 E2E 执行 agent 的模板**。MCP 支持 Streamable HTTP + legacy SSE，工具默认 `AskUser` 权限；自定义工具支持 `command/script/http/shell` 四种执行器；Auto agent 还能自助建 skill/tool/MCP。
`skills/playwright-cli/SKILL.md`、`skills/api-recon/SKILL.md`、`server/assembly.go:127-145`、`server/server_mgmt.go:1105-1131`、`server/platform_tools.go`、`mcphttp/client.go`
→ 迁移：直接建 `e2e-runner`/`web-explore`/`api-recon`/`evidence-pack`/`testcase-authoring` 五个技能；建议照搬目录结构（SKILL.md + scripts/ + references/ + assets/ + 完成定义），**脚本必须幂等、seed/cleanup 对称**。

---

### D. 人类在环与安全边界

**T16 高危操作审批门（intercept + guard）**
机制：唯一入口 `PreToolUse`：工具面过滤 → 规则（priority DESC，regex/子串，规则来自 DB 可运营）→ 未命中再走 LLM 裁判（严格 JSON：decision + comment 三段式；只判断当前调用的"直接、立即效果"，不注入历史/任务目标；失败策略默认 allow、ask 超时默认 deny）→ 三路 allow/deny/ask；ask 落 `intercept_pending` 持久记录并**阻塞**等待人工（先注册 channel 再回读 DB 防决策丢失；ctx 取消记 denied/not_executed；超时可配）；**两阶段审计 5 元组**（run_id/tool_use_id/参数 sha256、initial_action/effective_action/execution_status），`correlation=exact|ambiguous|unavailable`，"未执行"与"执行失败"严格区分，丢结果标 unknown 而非成功；block 结果加"平台管控·非目标防御"前缀防止执行 agent 误判为 WAF 而绕过。内置风险分档数据驱动：100=系统破坏（rm -rf/mkfs/dd/shutdown）、90=数据库破坏（DROP/TRUNCATE/FLUSHALL）、80=HTTP 破坏（DELETE/清除类路径）；v2/v3 演进显示"数据外泄管道默认关闭，避免误伤反弹 shell"。
`intercept/intercept.go:423-688`、`guard/guard.go`、`db/db.go:345-617`、`db/intercept_detail.go:19-114`、`intercept/prompt.go:10-128`
→ 迁移：E2E 的"删除数据/支付/发消息/生产发布/改配置"= deny 或 ask；**用例级预声明"需人工确认"**，超时默认 deny 并标 `skipped(needs-approval)`（否则会挂死 CI）；审批卡展示"实际操作/成功后果/命中规则"三段。

**T17 三粒度干预（半自主的关键操作杆）**
机制：任务级 pause/resume（暂停=停执行器+停 AI 对话+持久化；恢复重新走准入队列）；用例级 pause/resume/cancel（soft delete = 保留节点血缘只退出 frontier；hard delete = 级联删独占子孙；删除时必须填原因，且**删除原因会回流给 planner**）；运行中纠偏 `steer_work`（planner 把消息入 FIFO，worker 在**下一次工具调用前**被 hook 阻断并重规划，不杀进程）+ 人工 `worker_message`（要求意图已 paused → `paused→running` CAS → 独立 goroutine 从 transcript 续跑，不占池槽；`request_id` 幂等去重）。
`server/engine.go:424-633`、`server/task_control.go`、`server/intent_intervention.go`、`web/.../sessions-tab.tsx:539-632`
→ 迁移：测试执行中"这个断言应该是 403 不是 200""跳过这步""这条用例作废（理由 X）"都有对应通道；**先停写入、CAS 改状态、记录原因**是硬规则。

**T18 授权边界与范围最小化**
机制：`task_scope` 7 种 kind（company/root_domain/subdomain/ip/cidr/icp/keyword），`source ∈ auto|agent|manual`；auto 只从"worker 显式插入的资产"派生最小范围，**拒绝从工具副作用派生**（防止 AI 越测越大）；范围 = 覆盖度分母 + 授权边界；另有任务级 intercept 规则（任务白/黑名单优先于全局策略）。
`db/task_scope.go:11-120`、`server/task_intercept.go`、`db/asset_intercept.go`
→ 迁移：测试范围（环境/模块/接口/角色/数据）必须在澄清阶段固化；执行期自动扩范围要保守；"只测某环境、不碰生产"要有提示词之外的硬闸（网络/环境白名单）。

---

### E. 数据、证据与报告（E2E 的数据库骨架）

**T19 资产图建模（换成"被测应用模型"即可）**
机制：6 类节点各自"归一化自然键 + 部分唯一索引（`UNIQUE ... WHERE type=...`）+ `ON CONFLICT DO UPDATE` 合并"（标量取非空、数组 DISTINCT 合并）；`DomainKey`/`RootDomain`（eTLD+1）/`TemplatePath`（`/user/123 → /user/{id}`、UUID→`{uuid}`、长 token→`{token}`）全部程序算；父子/归属/去重由 db 层补写，agent 只交原始字段；归属优先级显式 > scope；`task_asset_links` 记录出处；资产 DSL（`field op value` + AND/OR/括号，白名单字段，裸词全文）+ **InScope 变体**（AI 查询默认限制在授权范围内）。
`db/schema.sql:27-148`、`db/assets.go`、`db/nkey.go`、`db/asset_dsl.go`、`db/task_asset_links`(schema.sql:648)
→ 迁移：节点类型换成 `page/route/api/component/dependency/spec/env`；**"路由模板化"必须第一天就做**，否则执行图会被 id 洪水淹没；查询 DSL 一定要有 InScope 变体。

**T20 执行记录与不可变证据链**
机制：`activity` 是逐步事件流（`thinking/text/user/tool_use/tool_result/usage/result/intent/round/llm_switch/llm_failover/intercept_request` 12 种 kind，`tool_use_id` 配对调用与结果，全局自增 id 作游标，token 只在终止 result 行）；证据 = 内容寻址不可变（元数据行 + `sha256(req/resp)` blob，先 staging 落盘校验后单事务提交，与"可清理的热流量库"物理分离）；`findings.evidence_version` 单调递增 vs `report_evidence_version` 记录报告依据版本，不等即 stale，写报告/绑定带 version 做 CAS（409）；导出先冻结 bindings/versions 再流式输出；GC 只回收无引用且过 24h 宽限的证据。
`db/schema.sql:298-339,1146-1174`、`evidence/store.go:33-144`、`db/finding_traffic.go:163-475`、`report/findings.go:178-200`
→ 迁移：**"运行产物（可清理）"与"证据（不可修改、可引用）"必须分层**；断言证据（截图/HTTP/日志/trace）一旦被用例结果或缺陷引用就冻结；报告记录"基于哪一版证据"。

**T21 缺陷、回归复测与归档**
机制：缺陷三层数据 = 探索图 `finding` 节点（payload/血缘）↔ `findings` 表（处置状态/severity/报告，跨任务可存活）↔ snapshot+binding（证据，role = baseline/proof/verification/supporting），双写有镜像入口；`finding_retests`：独立会话 + 创建时冻结 snapshot + 状态机 `pending→running→completed/failed/stopped` + 部分唯一索引保证同一缺陷同时最多一个 active 复测 + 三值结论 `reproduced/fixed/inconclusive`（"一次失败不能证明修好"）+ 重启把 pending/running 置 stopped + completed 无结论自动 failed；任务归档 = 22 张表 RepeatableRead 快照 + `manifest.json` + sha256 + v3 包 + 热点 stub 化 + 恢复按 FK 顺序重放 + 资产按自然键重映射（全局对象缺失降级 warning）。
`db/findings.go`、`db/finding_retests.go`、`db/task_archives*.go`、`server/task_archive_package.go`、`report/report.go`、`report/findings.go`
→ 迁移：**回归复测 agent 模板可直接照抄**（独立会话 + 上下文注入 + 最小复现 + 三态 + 证据门槛 + 环境指纹）；测试档案 = DB 快照 + 证据包 + 校验和 + 可恢复；缺陷/结果/证据之间的版本一致性用"双版本戳 + CAS"。

**T22 计量、审计与工程化迁移**
机制：`llm_usage` 每调用一行（成功失败都记，含 latency/tokens/cache/status，只存维度不存正文）vs `activity(kind='result')` 会话汇总（**两账本不可相加**）；`llm_records` 可选录制原始 wire body（含被丢弃的重试尝试，便于诊断限流/网关）；`skill_usage`/`tool_usage` 账本；启动幂等迁移 = `go:embed schema.sql` 每次全量执行 + advisory lock + 死锁退避重试 + 只做加法式 DDL + `DO $$` 检测式约束重建 + 一次性回填守卫（重启即迁移）；自更新 = 下载→SHA256SUMS 校验→`-h` 冒烟→暂存→换装→30s 观察→3 次启动失败回滚。
`db/llm_usage.go`、`llmrec/`、`db/db.go:20-87`、`db/schema.sql:198-274,456-474`、`selfupdate/`
→ 迁移：成本账本第一天就按 `case/phase/lane/model` 打标（ARTEX 没有单价字段，需自建费率表）；迁移脚本可重入、禁止"版本号条件分支"。

---

### F. 前端与产品形态

**T23 实时执行视图协议（最值得整段抄）**
机制：打开任务 → 先取最新一页历史（`snapshotCursor`）→ 在**同一 cursor** 上开唯一一条 `EventSource(?since=cursor)`：历史覆盖 ≤cursor、SSE 覆盖 >cursor，无缝无洞；`seq` 即身份，`mergeBySeq` 去重排序；服务端写 `id:` 行，断线自动带 `Last-Event-ID` 从 DB 续传；20s 心跳；`onerror` 只置"重连中"状态位，不伪造"没有新消息"；事件按 `session_key`（`main:<seg>`/`plan`/`intent:<id>`/`system`）路由到会话桶，非激活会话只留尾部 usage/result；单会话内存上限 4000 条；**无 optimistic echo**（用户消息由后端先落库再广播，保证每条都有真实 seq）。Activity 数据模型 = `seq/intent_id/worker/ts/kind/tool/tool_use_id/is_error/summary/detail/metadata/source_task_id/inherited/main_seg/tokens`。
`web/src/lib/api.ts:150-198`、`web/src/lib/types.ts:618-662`、`.../sessions-tab.tsx:872-1048,154-163`、`components/transcript.tsx:87-129`
→ 迁移：执行视图就用这套；额外给 DTO 补 `attempt/step_index`（重试/并行下线性 seq 无法解释顺序）。

**T24 信息架构、图与 HITL UI**
机制：任务详情 10 Tab（会话/总览/探索链路/播报板/发现/复测/测试资产/资产覆盖图/拦截审批/报告），顶部常驻暂停·停止·换模型·审批角标；两套图按语义选型——**探索链路 = React Flow + 自研分层 DAG**（DFS 断环→最长路分层→barycenter 四轮排序、轮询时保留节点位置、边语义四色、活边动画、digest 折叠重接边、MiniMap/Sheet 详情），**资产覆盖 = AntV G6 + d3-force**（父×类型每类折叠 20、+20 展开、已测实色/未测灰/范围外虚线、动态 import、stopLayout 后 destroy）；审批卡内联进对话流 + 可深链回执行现场（`?approval=` 反向翻页补齐历史）+ 保存"模型审查输入快照+哈希+执行关联"；工具调用折叠一行、展开看命令与输出（tool_use/result 按 id 配对、懒加载）；@提及（`@[类型#ID 名称]` 服务端解析成结构化快照，带体积上限与防注入声明）；`/btw` 旁路提问不污染主上下文；Worker 只能暂停后追发消息；删除分假删/真删并要求理由；单二进制静态导出 + 同源 SSE（dev 必须绕开 Next `/api` rewrite，否则 SSE 被缓冲成 0 事件）。
`web/src/app/(main)/function/tasks/detail/`、`components/exploration-graph.tsx`、`.../coverage-graph-tab.tsx`、`components/approval-records.tsx`、`components/transcript.tsx`、`lib/status.ts`
→ 迁移：**思维导图建议 React Flow + tidy tree**（节点=用例卡：前置/步骤/预期/优先级/覆盖需求 id；三态配色）；报告页是 ARTEX 明确短板（仅 Markdown+复制）——**这是最容易做出差异化的页面**（结构化 + 覆盖矩阵 + 证据内嵌 + 深链回现场 + 导出 MD/HTML/PDF）。

---

## 三、ARTEX → E2E 概念映射表

| ARTEX | E2E 测试系统 | 迁移要点 |
|---|---|---|
| 任务 Task（1:1 exploration） | 测试任务/测试轮次 run | 暂停/排队/截止与执行解耦 |
| goal（可验收结果） | 需求条目/验收标准 | `prove_goal` = 需求验收通过 |
| intent（开放探索方向） | 测试用例/测试步骤 | 需扩展"预期/断言"语义 |
| fact（observed/inferred） | 执行结果与断言事实 | 需扩展 pass/fail/inconclusive/blocked |
| finding（三层数据） | 缺陷单 | 必须绑真实执行证据 |
| hint | 人工测试策略/补充口径 | 折叠进每轮 overview |
| 资产图（全局） | 被测应用模型（页面/路由/API/组件/环境） | 自然键+幂等 upsert 照抄 |
| 探索图 | 需求→用例→结果→缺陷思维导图 | 5 类血缘边照抄 |
| anchors | 用例↔页面/接口↔需求 | 追溯矩阵最小关系表 |
| coverage（资产维度） | 需求/用例覆盖率 | **口径必须重定义** |
| planner / worker / mainagent | 用例设计器 / 执行器 / 澄清助手 | 职责边界照抄 |
| reporter / retester | 报告 agent / 回归复测 agent | 事件触发 + 三态结论 |
| coldgraph/digest | 历史用例与缺陷压缩上下文 | 展示层折叠、原节点保留 |
| intercept ask | 高危测试动作/生产操作确认 | 用例级预声明+超时 deny |
| traffic（MITM） | 网络层证据 | 证据与热流量分层 |
| task_archives | 测试档案包 | 快照+校验和+自然键恢复 |
| task_relations | 跨迭代历史只读继承 | 带 inherited 标记 |
| agent_triggers | 失败→复测/缺陷→报告/完成→通知 | 水位式触发 |
| llm_usage/llmrec | 成本账本 + 原始报文审计 | 按 case/phase/lane 打标 |

---

## 四、如果现在设计这套系统：骨架草案

**阶段**：项目/环境接入（代码索引、环境与凭据、应用模型）→ **需求澄清**（对话 + 候选/确认分离 + 约束/范围/验收标准 + 冲突检测 + 决策日志）→ **用例设计**（规划器读需求图+代码现状+历史缺陷+本次变更 → 生成用例图/思维导图 → 人工 review 计划）→ **执行**（用例级 agent 池 + 步骤级证据 + 高危审批 + 预算收尾）→ **缺陷/回归**（去重分级 + 报告 agent + 复测 agent 三态）→ **报告/归档**（覆盖矩阵 + 证据包 + 版本化）→ **自动化**（触发器/CI webhook/定时回归）。

**Agent**：clarify / designer(planner) / executor(worker) / triager / reporter / retester / assistant(auto)。

**核心表**：`project, environment, app_assets, requirements, cases, case_runs, run_events(activity), assertions, defects, evidence_snapshots, evidence_bindings, retests, coverage, digests, approvals, usage_ledger, archives`。

**前端 5 页**：需求澄清（对话+需求草稿+决策记录）/ 用例与思维导图 / 执行实时视图 / 报告 / 系统与审批中心。

**每条主线都要能回答**：这条需求 → 哪些用例 → 哪次执行 → 什么证据 → 哪个缺陷 → 是否回归 → 覆盖率多少。

---

## 五、ARTEX 的空白（必须新设计，不能借鉴）

1. **引导式头脑风暴澄清**：ARTEX 没有多轮澄清（只有一次性 goals 拆解 + 运行期 hint/set_goals）。缺：提问-回答-复述确认循环、候选（未确认）vs 确认需求的分状态、歧义维度清单（目标/范围/验收/数据/环境/风险/优先级/非功能）、冲突检测、需求变更→用例作废/重跑推导、澄清质量 checklist。
2. **代码现状感知的用例设计**：ARTEX 是黑盒攻击，无代码索引/影响分析。需自建 repo 索引（符号/路由/组件/依赖）、diff→影响面→测试选择、可测性缺口分析。
3. **思维导图**：ARTEX 只有图数据与只读可视化，没有树导出/编辑/版本/审批。
4. **测试执行语义**：ARTEX 的 fact 是开放结论，没有"预期/断言/步骤/verdict/flaky"；需断言引擎 + 证据绑定 + 四态结果。
5. **非幂等副作用治理**：ARTEX 按整条意图重跑（渗透幂等）；E2E 需步骤级 checkpoint、环境快照/回滚、数据 seed/teardown、隔离运行目录。
6. **需求↔用例↔结果↔缺陷可追溯与覆盖率口径**：ARTEX 的 coverage 是资产维度，不能直接当需求覆盖。
7. **结构化报告**：ARTEX 报告 = Markdown 文本（薄），需覆盖矩阵、证据内嵌、深链、导出、生成口径标注。
8. **成本/多租户/CI 集成**：无单价字段、单用户 JWT、审批 ask 会挂起流水线（需预授权清单+超时策略+skipped 状态）。
9. **测试数据与凭据生命周期**、**环境/版本指纹**（回归复测可信的前提）ARTEX 都没有对应物。

---

## 六、照抄会踩的坑 Top 12

1. **重试重复副作用**：意图级重跑对下单/写库/发消息是灾难 → 步骤级 checkpoint 或环境快照。
2. **提示词当边界**：ARTEX planner 工具集里其实有 Bash，靠提示词约束"只规划" → 新系统用工具白名单强制。
3. **todolist 只在内存**：重启即丢 → 落库或完全从图重建。
4. **冷压缩异步**：commit 不复核会覆盖"复活"热点 → 复活复核 + digest 质量门槛。
5. **双 ID 陷阱**：`finding_id` vs `finding_node_id` → 别设计同义 ID。
6. **提示与工具不同步**：说到的工具不存在 = 幻觉调用 → 提示段与装配同步 gate。
7. **审批 ask 挂死自动化**：用例级预声明 + 超时 deny + `skipped(needs-approval)`。
8. **把 SSE 当可靠通道**：ARTEX 明确"DB 是真值、广播有损" → 游标回放 + 去重 + 心跳。
9. **触发器水位与内存队列缺口**：先持久入队再推水位（ARTEX 现版此处有隐患）。
10. **双写不一致**：findings 表 ↔ finding 节点 payload、todo ↔ 图 → 单一写入口或同事务镜像。
11. **证据层混淆**：可清理的运行产物 vs 不可变证据必须物理分离；报告必须记版本。
12. **多 key 轮询破坏可复现性**：结果必须记录实际 model/profile，回归基线固定模型。
13. **上下文膨胀**：工具返回/@引用/trace 详情必须上限 + 落盘 + 分页。
14. **AGPL 合规**：借鉴设计可以，抄代码要开源（见开头提醒）。

---

## 七、借鉴优先级与下一步

- **P0（先做，决定系统能否跑起来）**：执行骨架（T1/T2/T3/T4）+ 双图与用例数据模型（T7/T19/T20）+ 执行实时视图（T23）+ 高危审批门（T16）。
- **P1（决定好不好用）**：澄清对话（新建）+ 用例设计器（T9/T13）+ 思维导图 UI（T24）+ 技能体系与 Playwright/API 技能（T15）+ 复测/报告 agent（T21）。
- **P2（决定可运维/可扩展）**：LLM 网关（T5/T22）+ 冷压缩与跨迭代继承（T10/T11）+ 归档/通知/自更新（T6/T21/T22）+ 成本/多租户。

---

## 八、后续深入提问索引（对应源码位置）

| 想深挖 | 看哪里 |
|---|---|
| 引擎/状态机/收尾 | `server/engine.go`、`server/engine_timeout.go`、`server/task_control.go` |
| 规划器与提示词 | `agent/planner.go:295-472`、`agent/promptcatalog.go` |
| 执行器生命周期 | `agent/worker.go:230-535`、`agent/wrapup.go` |
| 双图/锚点/覆盖度 | `db/exploration.go`、`db/task_scope.go`、`db/constants.go` |
| 证据/缺陷/复测/报告 | `evidence/store.go`、`db/finding_traffic.go`、`db/finding_retests.go`、`report/` |
| 审批门/审计 | `intercept/intercept.go`、`intercept/prompt.go`、`db/intercept_detail.go` |
| 前端实时协议 | `web/src/lib/api.ts`、`web/src/lib/types.ts`、`.../sessions-tab.tsx` |
| 图可视化 | `web/src/components/exploration-graph.tsx`、`.../coverage-graph-tab.tsx` |
| 技能体系 | `skills/playwright-cli/SKILL.md`、`skills/api-recon/SKILL.md`、`server/assembly.go` |
| LLM 网关/录制/重试 | `llmpool/`、`llmrec/`、`server/task_llm.go` |
| 归档/迁移/部署 | `db/task_archives*.go`、`db/db.go`、`selfupdate/`、`build.sh` |
| 登录/凭据/浏览器执行 | 见附录 A；核心：`agent/worker.go`、`server/assembly.go`、`skills/playwright-cli/`、`Dockerfile` |

---

## 附录 A：ARTEX 怎么做到端到端执行？——以"给账号密码自动登录"为例

> 本附录回答两个问题：① ARTEX 的"端到端"到底是怎么跑起来的；② 只给它一组账号密码，它如何自己完成登录。结论先行：**ARTEX 没有"登录模块"，也没有录制回放/断言 DSL；登录只是 worker agent 在执行某条 intent 时临时组合出的一串真实工具调用（curl / playwright-cli / 浏览器 MCP / 交互式 shell），账号密码则来自任务描述等自由文本输入。** 它的"端到端"= 从可验收目标到证据报告的一条自主执行链。

### A.1 一次任务的完整生命周期

1. **建任务**：UI 填「任务名称 / 任务描述 / 任务目标」（可选上传文件：文件绝对路径会追加到描述末尾，worker 之后可用 Read/Bash 打开），再选 LLM 配置、超时、worker 数。`web/src/app/(main)/function/tasks/page.tsx:3227-3363`、`server/server.go:1456,1533`。
2. **seed 根事实 + 目标分解（第 0 轮）**：`launchTask` 先写入探索图的根 fact（payload 含原始描述），异步跑 `createGoals`（一次 LLM 调用，MaxTurns=8）：从「目标 + 描述」里抽取①可验收目标 ②allow/deny 操作约束（必须自包含写死具体目标）③最小范围 `task_scope`，全部写入探索图；随后才 `engine.Run`。`server/goals.go:28-60`、`agent/goals.go:19-67,106-181`。
3. **引擎启动**：每任务 1 个 plannerLoop + N 个 workerLoop（N 在系统设置里配，默认 3）。`server/engine.go:748-777`。
4. **planner 规划**：每轮把态势预取进 user 输入（`out["task"] = {description, goal}`、goals、hints、资产计数、open/running/recent intents、facts/findings、coverage），生成 0..N 条 intent（summary + asset_ids + parent_ids），强制血缘挂到已存在的 fact/finding。**任务描述因此天然进入 planner 视野**。`agent/tools.go:513`、`agent/planner.go:355-472`。
5. **worker 执行**：领一条 intent；system 里带「当前意图 + 目标资产 + 操作约束 + 中间产物目录」，工具有 `Bash/Read/Write/Edit/LS`（+ 可选 `WebFetch/WebSearch`）、领域工具（`insert_assets/record_fact/report_finding/graph_overview/list_*` 等）、`traffic_*`、`shell_*`（交互式 shell，默认对 planner/worker/mainagent/auto 开启）、可选的 skills 与 MCP 工具；在真实环境里执行。`agent/worker.go:361-535`、`server/assembly.go:235-323`。
6. **全程留痕**：Bash 子进程环境注入记录型 MITM 代理与 CA（`HTTP(S)_PROXY`、`SSL_CERT_FILE` 等 6 个变量），WebFetch 也走代理；抓包开关打开时浏览器 MCP 同步注入 `--proxy-server` 与 `NODE_EXTRA_CA_CERTS`。因此登录请求/响应（含 Set-Cookie、token）可在 `traffic_search/traffic_get` 里回看。`agent/worker.go:202-228,437-453`、`server/manager.go:631-678`、`traffic/traffic.go`。
7. **边做边写回**：新资产 → `insert_assets`；结论/观察（含否定结论）→ `record_fact`（带 evidence 一行 + confidence=observed/inferred）；确认漏洞 → `report_finding`（必须本次真实触发、有可复现证据）。写回后引擎唤醒 planner 进入下一轮。`agent/worker.go:237-240`、`server/engine.go:1155`。
8. **收官与报告**：planner 用 `prove_goal` 逐条证明目标，全部 met 自动判任务完成；finding 触发独立的 reporter agent 写漏洞报告；`report/report.go` 从图生成任务 Markdown 报告；retester 做回归复测。`agent/planner.go:315-316`、`agent/tools.go:1184-1200`、`server/orchestration.go:731-780`、`report/report.go`。

### A.2 账号密码从哪来

| 来源 | 说明 | 位置 |
|---|---|---|
| 任务描述 / 目标 | 最常用：用户直接把账号密码写进描述（例如"后台 admin/Admin@123，验证订单流程"） | `agent/tools.go:513`（planner 可见）、worker 启动 user 消息里的 overview |
| 上传文件 | 建任务时上传的账号清单/接口文档，其绝对路径追加进描述，worker 用 Read/Bash 打开 | `web/.../tasks/page.tsx:3252-3274` |
| Hints | 人工/主 agent `add_hint` 写入的战略提示，每轮 overview 都会带上 | `agent/tools.go:365-378`、`agent/mainagent.go:90-96` |
| 资产 auth 字段 | 之前 worker 用 `insert_assets` 登记的认证信息（`auth`：type/username/password 等，追加不覆盖），后续 worker 查资产可见 | `db/schema.sql:75`、`agent/tools_insert.go:108,185-189` |
| 流量记录 | `traffic_search`（`body_contains` 支持中文/子串）在已抓流量里搜 password/token/cookie | `agent/worker.go:250-255`、`traffic/traffic.go` |
| 技能发现 | `api-recon` 技能从 JS bundle/运行时接口里发现认证接口与参数（注意：该技能**明确禁止真实登录**，仅做 recon） | `skills/api-recon/SKILL.md` |

> 注意：ARTEX **没有专门的 secret 存储**，账号密码会进入提示词、transcript、activity 与可选 LLM 报文录制。这是迁移到 EzTest 时必须重新设计的一层。

### A.3 登录动作是怎么执行的（三条路径）

**路径 A：HTTP/API 登录（最常见、最轻）**
worker 用 Bash 直接发请求（所有请求经 MITM 留痕），典型序列：
```bash
curl -i -c cookies.txt -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"Admin@123"}' http://target/api/login
curl -b cookies.txt http://target/api/orders        # 复用会话
```
或先 `WebFetch` 打开登录页确认表单字段/防爬，再决定用 curl。成功后 worker 把「会话已建立、cookie 存于 `<runDir>/cookies.txt`、证据是 Set-Cookie 头」写成 fact（`record_fact`），可把登录接口/凭据登记进资产图。中间产物按规约写在本次 intent 专属目录 `<workDir>/tasks/<taskID>/i<intentID>/`（`agent/worker.go:257-283`）。

**路径 B：浏览器 UI 登录（验证码、前端加密、SPA 时必须）**
镜像已预装 Node 20 + `@playwright/cli`（`playwright-cli`）+ `@playwright/mcp` + chromium（`Dockerfile:23-31`）。两条路：
- **playwright-cli（作为 Bash 命令，需给 agent 配 `playwright-cli` 技能）**：`playwright-cli open http://target/login` → `snapshot`（拿到页面快照与元素 ref，如 e15）→ `fill e15 "admin"` / `fill e16 "Admin@123"` → `click e17` → `snapshot` 确认进入后台 → `state-save auth.json`（保存 cookies+localStorage）；后续 `state-load auth.json` 恢复登录态；`-s=auth` 可命名会话隔离/并发登录。命令跨多次 Bash 调用共享同一浏览器 daemon 会话。`skills/playwright-cli/SKILL.md:8-24,116-145`、`references/session-management.md`、`references/storage-state.md`。
- **Playwright MCP（内置 `browser` MCP，默认禁用）**：启用并分配给 agent 后，模型直接调 `mcp__browser__*`（导航/点击/填表/快照）；抓包开关会同步代理与 CA，所以浏览器里的登录请求也会被记录。`db/db.go:255-265`、`server/manager.go:631-678`。

**路径 C：交互式 shell（SSH / MySQL / REPL / 密码提示）**
`interactive_shell` 对运行期 agent 默认开启，会注入 `shell_open/shell_send/shell_close`，并把「需要 stdin 的程序（ssh 交互登录 / mysql、psql、python REPL / 密码或 yes/no 提示 / msfconsole）不要用 Bash，改用 shell_open」写进 Bash 描述。`db/db.go:246-253`、`server/assembly.go:235-238,308-322`。

### A.4 登录成功如何判定、登录态如何在后续步骤复用

- **判定**：没有内置"登录断言"，由 agent 观察（HTTP 200/302、Set-Cookie、页面 title/URL、API 返回结构）后写 fact 的 evidence 文本；这是"LLM 自证"，不是程序断言。
- **跨 worker 的天然障碍**：每个 intent 是独立 worker 会话（session id 按 intent 确定、可 resume），run 目录也按 intent 隔离（`agent/worker.go:90-96,271-283,500-525`）。所以一个 worker 的浏览器 session/cookie 默认不会带到另一个 worker。
- **ARTEX 的复用方式**：① planner 用 TodoWrite 把「登录 → 后续操作」记成串行依赖链，后一条 intent 显式引用前一步产出（fact 里写文件路径/凭据）；② 登录结果写入资产 `auth` 或事实，后续 worker 通过 `list_assets/node_detail` 读取；③ 直接用 `traffic_search` 从已录流量里取 token/cookie；④ 多数渗透场景直接重新登录（成本低）。
- **人工兜底**：人可以看 worker 每一步；暂停 worker 后追加消息（如"验证码是 1234"）再续跑；或 `add_intent` 直投一条"用某账号登录"的意图；或 `add_hint` 给 planner。危险命令（rm/DROP/DELETE 等）会触发审批门阻塞等待人工允许/拒绝。
- **AI 主动问人**：ARTEX **没有**"向人类提问"的工具（只有平台审批 ask）。执行阶段遇到 2FA/验证码/缺凭据时，只能记 blocked/inferred fact 等人看图干预——这是 EzTest 要补的能力。

### A.5 因此，它"算不算端到端测试"

- ARTEX 本质是**目标驱动的自主渗透探索**：不录脚本、不回放、无断言 DSL、无用例 pass/fail 模型、无测试数据管理；它的"端到端"指"从目标到证据报告的一条自主执行链"，登录只是链上的一步。
- 对 EzTest 的启示：**保留 ARTEX 的执行底座**（事件流 / 双图写回 / 证据 / 审批 / 三粒度干预 / 预算收尾 / 触发式专职 agent），但**把"登录/会话态"升为一等公民**：显式的 `Session`/前置条件资产（storageState 文件、token、环境指纹）、用例步骤级 checkpoint、可复现的断言与证据绑定。把"给账号密码"从"写进提示词让 agent 现编"变成"结构化输入 + 会话夹具 + 可复用/可清理的生命周期"。
- 实践上，若直接让 ARTEX 登录，推荐的任务描述写法：`目标：https://target；登录入口 /login；账号 admin / Admin@123；范围：只测该站点，禁止改真实数据；验收：进入后台并读取当前用户信息`。账号密码写进描述（或上传的凭据文件路径），约束让 goals 抽成 allow/deny，danger 操作由审批门兜底。

---

*研究日期：2026-10-25；源码基线见文首。本文档为静态阅读结论，未经运行验证。*
