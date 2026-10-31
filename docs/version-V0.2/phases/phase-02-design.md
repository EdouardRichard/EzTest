# Phase P2 — 设计

> 阶段计划，不是任务计划。会话启动使用 [implementation-phase-plan.md](../implementation-phase-plan.md) 顶部提示词；PHASE_ID=P2；PHASE_FILE=phase-02-design.md；PREV_HANDOFF=phase-01-handoff.md。

## 目标

打通“新建设计对话 → 设计团队扫描待设计需求 → 实时脑图 → 用户对话/确认 → 导出”的垂直闭环。

## 输入

- 架构设计 §6.3、§7、§8.4、§11、§15
- 概要设计 §2（设计/用例表）、§3、§4、§5、§6.4、§7、§8
- 决策日志 D20–D27、D25 A+
- P1 handoff/contracts
- UI 草图用例设计页

## 范围（In）

- scenario/case_entity/case_version/step/assertion/source/confirmation；design_round/design_scope。
- 设计 Host/Planner/Author/Critic/Data Planner；结构化图提交 + 服务端校验；图增量事件/实时渲染。
- 需求→场景→用例横向脑图；用例详情；逐用例/场景/需求确认；导出 Markdown/Mermaid/OPML/CSV。
- D25 A+：默认新需求需新设计对话；当前轮次自动并入；变化/缺口自动补；版本级自动开关。
- stale/superseded 状态机；设计待办 CTA；跨 SUT 用例卡片。

## 不在范围（Out）

- 执行/波次/证据（P3）。
- 代码智能/视觉（P5）。
- P9 报告/知识/GC/多实例（P7）。

## 交付物

- 设计对话 + 脑图 + 用例详情/确认；设计轮次/scope；导出。
- 用例版本/确认/失效规则；跨 SUT 目标；设计自审。
- `contracts` 更新（design/task/tool/event）；`phase-02-handoff.md`。

## 依赖与前置

- P1 完成：需求版本/确认、任务板、Agent runtime、上下文、对话。
- P0 完成：版本/多 SUT/页面/API 绑定。

## 验收/演示

1. 已澄清需求 → 新建设计对话 → 团队扫描 → 初版脑图实时渲染。
2. 对话修改 → 新增/修改 draft → 用户确认（单用例/场景/需求）。
3. 需求版本变化 → stale → 重设计；新 draft 不改变旧 confirmed 状态，新版本确认后旧版 superseded。
4. 导出可用；D25 开关/待设计 CTA 可验证。
5. 测试全绿；contracts frozen。

## Handoff 必须包含

- 设计/用例表/状态机/确认 API；图增量事件；导出契约。
- P3 需要的输入：current+confirmed 用例版本、目标 SUT/页面/API、design_scope。
- 已知风险（图编辑并发、实时渲染、用例膨胀）。

## 风险/约束

- 不要恢复整版冻结/设计评审页签；确认粒度是用例/场景/需求。
- 用例步骤可跨 SUT，但执行上下文在 P3；P2 只产出目标字段。
- AI 不能确认用例；确认必须用户/服务端。
