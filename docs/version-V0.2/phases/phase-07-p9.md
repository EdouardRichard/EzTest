# Phase P7 — P9

> 阶段计划，不是任务计划。会话启动使用 [implementation-phase-plan.md](../implementation-phase-plan.md) 顶部提示词；PHASE_ID=P7；PHASE_FILE=phase-07-p9.md；PREV_HANDOFF=phase-06-handoff.md。

## 目标

补齐 0.2 的 P9 五项：D59 报告 PDF/模板、D63 跨版本知识、D65 GC、D67 多实例、问题重开/合并/跨波次去重。

## 输入

- 架构设计 §12、§13
- 概要设计 §9、§10
- 决策日志 D32、D33、D49–D53
- 参数默认（OD-2/3/4/8/9/11）
- P6 handoff/contracts

## 范围（In）

- **D59**：report_revision/HTML/PDF + 独立 headless render worker + 模板/章节开关 + 重试/索引。
- **D63**：global/system knowledge、显式晋升、审核、注入、撤回/复审；澄清/设计注入预算。
- **D65**：统一 content ledger GC（附件/导出/报告/孤儿）、dry-run、引用保护、24h 宽限、保留期、tombstone/审计。
- **D67**：多副本安全 + 基础编排（lease/fencing、PG LISTEN/NOTIFY、共享存储、readiness/排空/滚动升级、执行器注册/容量/离线重派）。
- **问题**：reopen/merge/alias/跨波次去重；旧报告 as-of；合并后复发的重定向。
- 0.1 数据只读归档/导出脚本（可选交付）。

## 不在范围（Out）

- 0.3 新能力（多用户协作、非 PG、外部工单/CI/IM、自动扩缩容/多区域 HA）。
- 重新设计主控制面（P1–P6 已冻结）。

## 交付物

- PDF 报告/模板；全局知识页/晋升/注入；GC dry-run/引用保护/审计；多副本部署/演练；问题重开/合并/去重。
- 迁移/契约/配置更新；P9 验收证据；`phase-07-handoff.md`。

## 依赖与前置

- P4 报告/问题；P5 代码/视觉；P6 质量/搜索/待办/Agent 中心。
- P1 任务板/租约/事件；P0 存储账本；P3 执行器注册/上下文。
- 多副本需要共享文件存储与 PostgreSQL；如部署环境不满足，停止报告，不降级实现。

## 验收/演示

1. 报告 HTML/PDF、模板/章节、revision/as-of/stale。
2. 知识晋升/审核/注入/撤回；GC dry-run/引用保护/保留期。
3. 多副本：加副本/滚动升级/故障恢复不重复执行/不批量中断；SSE 跨实例；执行器离线重派。
4. 问题重开/合并/去重/旧报告 as-of。
5. 测试/演练全绿；contracts frozen。

## Handoff 必须包含

- P9 表/API/worker/部署契约；保留期/模板/知识 scope 参数。
- P8 需要的输入：P9 验收证据、多副本演练结果、GC 报告。
- 已知风险（D67 多副本、GC 误删、报告/问题一致性）。

## 风险/约束

- GC 默认 dry-run；引用保护优先；旧 report revision 不自动删。
- 多副本安全优先于“多活编排”；不做自动扩缩容。
- 知识不做向量 RAG；注入带引用与预算。
