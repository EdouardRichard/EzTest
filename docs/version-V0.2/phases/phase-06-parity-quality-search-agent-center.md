# Phase P6 — 全功能补齐

> 阶段计划，不是任务计划。会话启动使用 [implementation-phase-plan.md](../implementation-phase-plan.md) 顶部提示词；PHASE_ID=P6；PHASE_FILE=phase-06-parity-quality-search-agent-center.md；PREV_HANDOFF=phase-05-handoff.md。

## 目标

补齐 P0–P8 剩余能力：质量/评测、全局搜索、待办中心、Agent 中心、链路增强与全局中心完善，确保 0.1 能力矩阵逐项有 0.2 入口。

## 输入

- 架构设计 §7.1、§15、§16
- 概要设计 §1.4、§2（quality/chain/search/workspace 表或无表读模型）、§7、§8
- 决策日志 D37、D45–D48、D53–D58
- P5 handoff/contracts

## 范围（In）

- quality/eval_run/eval_metric/质量中心 UI；指标/阈值/基线。
- search 跨实体读模型/API/UI。
- todos 待办中心（评审/接管/设计待办/人工请求/human_request）。
- Agent 中心（目录/运行观察/配置/质量指标）。
- 全局问题/报告中心完善；探索链路 v2 增强（预算/截断/重连/回放细化）。
- 全功能能力矩阵逐项入口/验收。

## 不在范围（Out）

- P7 P9（PDF/知识/GC/多实例/问题重开合并增强）。
- 0.3 新能力。

## 交付物

- 质量/搜索/待办/Agent 中心/全局中心/链路增强的 API/UI/测试。
- 能力矩阵状态更新（每项入口/验收）。
- `contracts` 更新；`phase-06-handoff.md`。

## 依赖与前置

- P4 交付/链路、P5 代码/视觉、P1/P3 运行时/执行。
- 若某项能力依赖 P7（如跨版本知识），只做入口/占位，不提前实现。

## 验收/演示

1. 质量中心可看指标/基线；搜索跨实体；待办覆盖评审/接管/设计待办/人工请求。
2. Agent 中心可看目录/运行/质量；全局问题/报告中心可用。
3. 链路增强可验证；P0–P8 能力矩阵无缺口。
4. 测试全绿；contracts frozen。

## Handoff 必须包含

- 质量/搜索/待办/Agent 中心契约；能力矩阵状态。
- P7 需要的输入：指标/搜索/待办/运行数据/链路接口。
- 已知风险（入口分散、验收面广、性能）。

## 风险/约束

- 不为了补齐而恢复 0.1 旧页面结构；按 0.2 8 Tab/全局壳。
- 搜索/待办/链路是只读读模型；不写业务表。
- 评测指标由服务端派生，不信任客户端 verdict。
