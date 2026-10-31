# Phase P5 — 代码智能 + 视觉断言

> 阶段计划，不是任务计划。会话启动使用 [implementation-phase-plan.md](../implementation-phase-plan.md) 顶部提示词；PHASE_ID=P5；PHASE_FILE=phase-05-codeintel-visual.md；PREV_HANDOFF=phase-04-handoff.md。

## 目标

补齐 0.1 P0–P8 的差异化能力：代码索引/diff/影响/锚点/事实复核，以及视觉基线/审批/差异。

## 输入

- 架构设计 §6.3、§7.3、§8.4–§8.5、§17
- 概要设计 §1.4、§2（代码/视觉表）、§4、§6.4、§7、§8
- 决策日志 D20–D27、D39–D44
- P4 handoff/contracts
- 0.1 代码智能/视觉验收记录（只作参考，不继承旧架构）

## 范围（In）

- code_index_run/entity/edge/fact/analysis/impact/anchor/fact_review；Git 索引/diff；影响候选；代码锚点；事实人工修正。
- visual_baseline/visual_assertion/visual_diff；基线审批；actual/diff 证据；四态分类。
- 设计/执行 Agent 工具接入代码事实与视觉；代码与变更 UI；执行 Tab 视觉子视图 + 质量中心指标。
- AI 页面/API 提议（基于代码/探测）→ 管理员/用户确认后写入 SUT 页面/API 清单。
- 问题/用例/需求代码锚点校验。

## 不在范围（Out）

- P6 质量/搜索/待办/Agent 中心（除视觉指标占位）。
- P7 P9。

## 交付物

- 代码索引/影响/锚点 API/表/UI；视觉基线/差异 API/表/UI。
- 设计/执行工具接入；测试语料/验收证据。
- `contracts` 更新；`phase-05-handoff.md`。

## 依赖与前置

- P2 设计：用例/需求版本、目标 SUT/页面/API。
- P3 执行：证据/结果。
- P4 交付：问题/报告/链路。
- P0 资源：代码仓、页面/API。

## 验收/演示

1. 索引本地 Git 仓 → 设计读到代码事实/路由/API；影响分析产出候选。
2. 问题/用例可带 `file:line` 锚点；事实人工修正可审计。
3. 视觉基线审批 → 执行 actual/diff → 四态分类/证据。
4. 测试全绿；contracts frozen。

## Handoff 必须包含

- 代码/视觉表/API/工具契约；锚点校验规则；视觉四态分类。
- P6/P7 需要的输入：代码事实、锚点、视觉证据/指标。
- 已知风险（代码语言无关精度、视觉稳定性、证据体积）。

## 风险/约束

- 不引入 parser/符号级解析器；保持语言无关 + 证据校验。
- 代码仓只读；不切分支/不改工作树。
- 视觉断言只对已确认用例版本生效；stale 用例不执行。
