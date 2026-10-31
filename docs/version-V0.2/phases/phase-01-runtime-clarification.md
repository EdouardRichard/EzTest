# Phase P1 — 控制面 + 澄清

> 阶段计划，不是任务计划。会话启动使用 [implementation-phase-plan.md](../implementation-phase-plan.md) 顶部提示词；PHASE_ID=P1；PHASE_FILE=phase-01-runtime-clarification.md；PREV_HANDOFF=phase-00-handoff.md。

## 目标

建立共享任务板/租约/reconciler、Agent runtime/上下文、团队频道；打通“新增对话 → AI 澄清 → 需求版本 → 用户确认/回退”的垂直闭环。

## 输入

- 架构设计 §5、§6.2、§7、§8.3、§9
- 概要设计 §2（版本/需求/澄清/Agent/运行时表）、§3、§4、§5、§6.3、§7、§8
- 决策日志 D14–D19、D35–D44、D49–D51
- P0 handoff/contracts
- UI 草图需求与澄清页

## 范围（In）

- work_item/lease/fencing/reconciler；team_room/team_room_digest；agent_profile/run/message/context_packet。
- conversation/message/summary；requirement/requirement_version/requirement_confirmation/relation。
- 附件上传/解析（doc/pdf/md/ppt）；澄清 Host/Curator/Scout。
- 需求列表/编辑器/批量确认/AI 回退卡；上下文分层/压缩/工具结果落盘。
- 版本状态投影 initial/running；澄清 UI；fake LLM/真实 LLM 测试路径。

## 不在范围（Out）

- 用例/脑图/设计团队（P2）。
- 执行/波次/证据（P3）。
- 代码智能/视觉/质量/搜索/待办/Agent 中心（P5/P6）。

## 交付物

- 任务板可领取/续租/重试/恢复；团队频道/摘要；agent run 作用域注入（模型不传 versionId）。
- 澄清对话主 UI + 需求列表/编辑；AI 建 draft、用户确认/批量、AI 回退；关联用例 stale 规则（P2 消费）。
- 上下文包/摘要；长会话 stress 测试；context_overflow 恢复。
- `contracts/task-types.md`、`agent-profiles-tools.md` 更新；`phase-01-handoff.md`。

## 依赖与前置

- P0 完成：资源/版本/设置/事件/存储/壳/contracts。
- 若 P0 的资源/版本契约缺失，停止并报告。

## 验收/演示

1. 用户新建对话 → AI 读取版本上下文/附件 → 产出 draft 需求。
2. 用户编辑/单条/批量确认 → AI 可回退 → stale 标记。
3. 任务板并发领取不重复；崩溃/租约过期可恢复；上下文压缩后关键决定不丢。
4. 测试全绿；contracts frozen。

## Handoff 必须包含

- 任务板/租约/reconciler 契约；Agent 工具作用域注入；上下文包字段。
- 需求/确认/回退 API 与表；对话/消息/摘要契约。
- P2 需要的输入：需求版本、确认、stale、design_scope 占位。
- 已知风险（上下文摘要质量、附件解析、并发写需求）。

## 风险/约束

- 不要让 Agent 直接用 versionId 作为工具参数；作用域由 runtime 注入。
- 不要让聊天全文进入任务上下文；只给摘要/钉住事实/相关产物。
- 任务板/reconciler 必须幂等；重复扫描不产生重复任务。
