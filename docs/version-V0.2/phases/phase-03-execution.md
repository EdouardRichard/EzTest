# Phase P3 — 执行

> 阶段计划，不是任务计划。会话启动使用 [implementation-phase-plan.md](../implementation-phase-plan.md) 顶部提示词；PHASE_ID=P3；PHASE_FILE=phase-03-execution.md；PREV_HANDOFF=phase-02-handoff.md。

## 目标

打通“已确认用例 → execution_intent → 自动波次 → 多 SUT 执行 → 证据/四态结果 → 失败/覆盖分析”的闭环。

## 输入

- 架构设计 §6.4、§7、§8.5、§11、§13
- 概要设计 §2（执行表）、§3、§5、§6.5、§7、§8
- 决策日志 D28–D31、D12/D13、D35–D44
- P2 handoff/contracts
- UI 草图执行区（结构）

## 范围（In）

- execution_intent/run_wave/case_run/case_run_sut_context/step_result/assertion_result/evidence。
- Execution Planner/Case Executor/Failure Analyst/Coverage Analyst。
- 多 SUT 浏览器上下文/账号角色/页面/API 路由；DB 断言；证据采集。
- 自动波次、单活跃波（含 paused）、intent 取消/重跑、暂停/恢复、登录接管 human_request、执行器注册/心跳/容量/排空/fencing、离线重派。
- 失败分类/问题候选、覆盖缺口→design.gap；执行 UI。

## 不在范围（Out）

- 问题单/报告/链路（P4）。
- 代码智能/视觉（P5）。
- P9 多副本部署编排（P7，但 lease/fencing 在 P1/P3 就位）。

## 交付物

- 执行/波次/结果/证据表与 API；executor 协议扩展（多 SUT 上下文）。
- 执行 UI：波次/实时/结果/证据/暂停恢复/登录接管。
- 四态结果、失败分类、覆盖缺口；intent 幂等/取消/重跑。
- `contracts/executor-protocol.md`、`task-types.md` 更新；`phase-03-handoff.md`。

## 依赖与前置

- P2 完成：current+confirmed 用例版本、目标 SUT/页面/API、确认。
- P1 完成：任务板/租约/Agent runtime/上下文。
- P0 完成：执行器/资源/账号/DB 绑定。

## 验收/演示

1. 确认用例自动入队/组波；单 SUT 用例可执行。
2. 跨系统用例在多个 SUT 上下文执行，证据/结果/失败分类带 `version_sut_id`。
3. 暂停/恢复/重派/租约过期可验证；重复确认不重复执行。
4. stale/superseded 用例取消 active intent/在飞 case_run。
5. 测试全绿；contracts frozen。

## Handoff 必须包含

- 执行表/API/executor 协议/intent 唯一键/波次并发默认（全局 3/每 version_sut 2/跨系统 1）。
- P4 需要的输入：波次/结果/证据/失败分类/覆盖缺口。
- 已知风险（跨系统执行、证据体积、执行器离线）。

## 风险/约束

- 不要恢复“人建批次/单活跃批次”旧模型；意图驱动、自动波次。
- 跨系统用例一次性预留 SUT 配额，避免死锁；默认低并发。
- 证据不可变；执行器迟到写入必须被 epoch/token 拒绝。
