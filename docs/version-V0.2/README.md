# EzTest 0.2 设计资料

> 状态：设计收口（2026-10-31）。用户已审核通过；独立评审 P0/P1 修订已并入；11 项参数默认已全部关闭。可进入 Superpowers spec/plan（本会话不产出）。
> 定位：AI 驱动、半自主、端到端测试系统；0.2 为完整替代版（0.1 P0–P8 全功能对齐 + P9 五项）。

## 文档索引

| 文档 | 内容 | 状态 |
|---|---|---|
| [architecture-design.md](architecture-design.md) | 0.2 架构设计：控制面/团队运行时/领域模型/流程/上下文/配置/P9/多实例 | 已评审（讨论稿） |
| [high-level-design.md](high-level-design.md) | 概要设计：模块/表/任务/工具/接口/前端/测试/依赖顺序 | 已评审（讨论稿） |
| [research/decision-log.md](research/decision-log.md) | 已拍板/推导/假设的设计决策，含参数默认 | 已评审 |
| [research/capability-matrix.md](research/capability-matrix.md) | 0.1 能力矩阵与 0.2 处置；已按“选项 C 全功能对齐 + P9”更新决策 | 研究稿 |
| [research/open-decisions.md](research/open-decisions.md) | 0.2 参数决策（已关闭）：导出/P9/知识/GC/迁移/账号/URL/并发/频道摘要/需求字段/报告种类 | 已定 |
| [implementation-phase-plan.md](implementation-phase-plan.md) | 实施阶段计划：启动提示词、WU、依赖图、P0–P8、阶段 handoff/contracts 机制 | **已批准** |
| [phases/](phases/) | 9 个阶段文件；每个会话启动一个阶段 | 计划 |
| [contracts/](contracts/) | 阶段间契约与 handoff 模板 | 机制 |
| [ui/mock.html](ui/mock.html) | 临时 UI 草图（需求澄清 / 用例设计 / 探索链路） | 草图 |
| [ui/requirements.png](ui/requirements.png) | 需求与澄清页 | 草图 |
| [ui/design.png](ui/design.png) | 用例设计页 | 草图 |
| [ui/chain.png](ui/chain.png) | 探索链路页 | 草图 |

> UI 草图只覆盖三个主页面（需求澄清 / 用例设计 / 探索链路）。执行工作区、视觉断言、待办抽屉、质量中心/Agent 中心/设置页的入口见 [high-level-design.md](high-level-design.md) §8；未在草图中绘制。

## 一句话架构

> 人只做三件事：澄清对话、开设计对话、确认用例；其余由 Agent 团队通过“共享任务板 + 版本×团队频道 + 产物交接”自主推进；版本状态只是投影，不是门禁。

## 核心决策快照

- 0.2 **全量重写**，0.1 仅参考；交付基线 = 0.1 P0–P8 全功能 + P9 五项。
- 版本状态：`initial / running / completed`，Agent 收尾，新目标自动重开；完成=过程闭环。
- 需求：逻辑对象 + 不可变版本；澄清对话主 UI；用户单条/批量确认；AI 可回退。
- 用例：需求→场景→用例三级；用例版本化；逐用例/场景/需求确认；执行只领最新已确认版本。
- 设计：设计页即 AI 对话；点“新建对话”开启一轮；设计团队自己扫描待设计需求；执行缺口/需求变化自动补 draft；版本级 `auto_design_new_requirements` 开关可让新需求自动开轮（默认关）。
- 执行：确认即入队；自动波次；同版本单活跃波；跨系统用例步骤级指定 SUT。
- Agent：四团队 + 专业 worker + Version Steward；共享任务板/租约/团队频道；无中心编排 Agent。
- 上下文：版本包/任务包/会话摘要/团队频道摘要/工具证据；token 预算；context_overflow 不再判失败。
- 资源：一版本多 SUT；SUT 多环境/多页面/多 API；版本可绑多库/多账号；密码 AI 可读。
- 配置：`.env` 只留 4 项（DB/API/Web/Executor 端口），其余进 DB 设置中心。
- UI：全局壳 8 项；版本详情 8 Tab（含探索链路一级 Tab）；知识全局。
- P9：D59 HTML/PDF、D63 全局知识、D65 统一 GC、D67 多副本安全、问题重开/合并/跨波次去重。

## 独立评审修订（2026-10-31）

已并入一轮只读一致性评审的 P0/P1 修订：

- capability-matrix 改判为 **0.1 P0–P8 全功能对齐 + P9**，不再后置代码智能/视觉/质量/搜索/Agent 中心。
- 统一 CaseVersion 状态机：需求变化→stale，新版本确认后旧 confirmed→superseded，执行只认 current+confirmed。
- 新增 `execution_intent`，驱动自动执行/重跑/收尾，避免重复执行或永不重跑。
- 统一执行层 `version_sut_id` 标识，补默认 SUT、账号角色、多 SUT 上下文。
- 补 `requirement_confirmation`、`case_confirmation`、`requirement_closure`、`requirement.goal_met`。
- 报告改 `report` + append-only `report_revision` + `report.render_pdf`。
- 新增 `export` 实体与导出 API，明确 `content_ref` 为唯一 GC 账本。
- 新增执行器注册/心跳/容量/排空与 `dispatch_epoch` fencing。
- 补全 P0–P8 → 0.2 能力映射、visual/quality/search/todos/agent-center/global issue-report 的 API/UI/验收。
- 统一版本状态迁移表与收尾谓词；补 human_request API/UI/流程；知识 scope/暂停/合并别名等 P2 修订。
- D25 拍板为 **A+**：默认新需求需新设计对话；变化/缺口自动补；当前轮次自动并入；版本级 `auto_design_new_requirements` 开关（默认关）。
- 第二轮复审残留 P1 已修：收尾谓词统一（waiting_human/human_request/paused/report.version）、execution_intent 唯一键与退出取消、visual/todos UI 入口、report revision append-only 语义、开放清单收敛、版本域事件 `version_sut_id`、paused 活跃索引、design_scope、执行器离线重派、严格 4 项 env。

## 参数默认（已定）

11 项参数已于 2026-10-31 **全部按推荐关闭**，详见 [open-decisions.md](research/open-decisions.md)。摘要：

1. 导出：Markdown/Mermaid/OPML/CSV；Excel 后置。
2. 报告：HTML 权威 + 独立 headless 渲染 PDF；内置模板 + 章节开关。
3. 知识：global + system；显式晋升 + 审核；PG 全文。
4. GC：统一 content ledger；dry-run；24h 宽限；旧报告 revision 不自动删。
5. 0.1 数据：新库，不原地迁移。
6. 账号：角色解析（version_sut + role）。
7. 页面/API：完整 URL + route_template；管理员确认 AI 提议。
8. 多 SUT：全局 + 每 version_sut 配额；跨系统默认低并发。
9. 团队频道：20 条/10 分钟摘要；保留最近 N 版。
10. 需求字段：必填 title/description/acceptance/priority；推荐结构化补充。
11. 报告：波次 + 版本总结；收尾前生成；均可 HTML/PDF。

## 阅读顺序

1. 先看 [decision-log.md](research/decision-log.md) 了解已拍板边界。
2. 再看 [architecture-design.md](architecture-design.md) 的 §1–§8，理解控制面/团队/流程。
3. 看 [high-level-design.md](high-level-design.md) 的 §2–§8，落到模块/表/接口/前端。
4. 对照 [UI 草图](ui/mock.html) 看用户旅程。
5. 最后看 P9/多实例/测试章节与 [参数默认](research/open-decisions.md)。

## 下一步

- 设计评审已通过；11 项参数默认已关闭。
- 实施阶段计划见 [implementation-phase-plan.md](implementation-phase-plan.md)：按 P0–P8 顺序，每个阶段一个会话，使用顶部提示词启动。
- 每个阶段结束读/写 [phases/](phases/) 下的 handoff，并更新 [contracts/](contracts/)；未交接不得进入下一阶段。
- 如后续要调整参数，按变更流程回写本目录并记录决策。
