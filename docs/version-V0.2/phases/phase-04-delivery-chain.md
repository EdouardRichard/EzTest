# Phase P4 — 交付 + 链路

> 阶段计划，不是任务计划。会话启动使用 [implementation-phase-plan.md](../implementation-phase-plan.md) 顶部提示词；PHASE_ID=P4；PHASE_FILE=phase-04-delivery-chain.md；PREV_HANDOFF=phase-03-handoff.md。

## 目标

打通“结果 → 问题 → 波次/版本报告 → 探索链路 → 版本收尾/重开”的交付闭环。

## 输入

- 架构设计 §6.4–§6.5、§8.6–§8.8、§12.1（基础 HTML）、§15
- 概要设计 §2（交付表）、§3、§5、§6.6–§6.8、§7、§8
- 决策日志 D32、D33（基础）、D45–D48
- P3 handoff/contracts
- UI 草图探索链路页

## 范围（In）

- issue/issue_ref/issue_status_history/analysis_run；问题确认/驳回/基础合并。
- report/report_revision（HTML/revision/as-of/stale）；`report.wave`/`report.version`；版本内问题/报告 Tab。
- chain 读模型 + 探索链路 UI：需求→用例→执行→证据/问题→报告→闭环；SUT/页面/波次过滤、实时、回放、深链。
- Version Steward + close 谓词 + report.version + 重开扫描；设计待办/收尾阻断。
- `human_request` 待办入口。

## 不在范围（Out）

- PDF/模板/headless render worker（P7 D59）。
- 跨版本知识/GC/多实例/问题重开合并增强（P7）。
- 代码智能/视觉（P5）。

## 交付物

- 问题/报告/链路表与 API；报告 HTML/revision；版本内问题/报告 Tab。
- 链路 UI + 读模型；问题/报告 Tab；版本收尾/重开。
- `contracts/openapi/events/db-schema` 更新；`phase-04-handoff.md`。

## 依赖与前置

- P3 完成：波次/结果/证据/失败分类/覆盖缺口。
- P2/P1：用例/需求/确认/版本。
- P0：存储/事件/版本绑定。

## 验收/演示

1. 波次结束 → 问题候选/确认/驳回；报告 revision/as-of/stale。
2. 链路实时展示需求→闭环，支持 SUT/页面/波次过滤、深链、回放。
3. 版本收尾：无开放任务/intent/wave/human_request + 需求闭环 + report.version published → completed；新目标自动回 running。
4. 测试全绿；contracts frozen。

## Handoff 必须包含

- 问题/报告/链路契约；report revision/状态；close 谓词。
- P5/P7 需要的输入：报告 revision、问题、代码/视觉锚点、链路读模型。
- 已知风险（报告 as-of、链路预算/截断、收尾竞态）。

## 风险/约束

- 报告内容确定性；LLM 只写叙述候选。
- 链路读模型只读，不写业务表。
- 旧报告 revision 不覆盖；问题合并先做基础，P7 增强。
