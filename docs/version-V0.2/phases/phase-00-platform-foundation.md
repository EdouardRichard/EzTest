# Phase P0 — 平台底座与契约

> 阶段计划，不是任务计划。会话启动使用 [implementation-phase-plan.md](../implementation-phase-plan.md) 顶部提示词；PHASE_ID=P0；PHASE_FILE=phase-00-platform-foundation.md；PREV_HANDOFF=无。

## 目标

建立 0.2 的新地基：4 项 env + DB 设置中心、身份、LLM、事件/SSE、内容账本、审计、资源域（SUT/环境/页面/API/库/账号/代码仓）、版本与多 SUT 绑定、前端壳/IA、迁移与测试骨架、contracts 骨架。

## 输入

- 架构设计 §1–§6、§9–§10、§13–§15
- 概要设计 §1–§2、§6.1–§6.2、§7、§8、§10
- 决策日志 D01–D13a、D49–D53
- 参数默认（open-decisions）OD-1/3/4/5/6/7
- UI 草图（壳/资源/版本详情结构）

## 范围（In）

- 新 Go 模块骨架、四层目录与装配根；迁移框架与 order。
- `system_setting`/设置中心 API/UI；严格 4 项启动 env。
- identity/auth；LLM 配置与模型目录；eventing/SSE；storage/content_ref；audit。
- resource：SUT/环境/页面/API/目标库/测试账号/代码仓；页面/API 手工 CRUD/导入（AI 提议在 P5 接入）。
- version/version_sut/version_sut_resource 绑定；版本列表/创建/详情壳。
- 前端全局壳 8 项、版本详情 8 Tab 骨架、资源页、设置页。
- contracts 骨架、fake LLM、测试骨架。

## 不在范围（Out）

- 任务板/Agent runtime/对话/需求/设计/执行/交付（P1+）。
- 任何 0.1 数据迁移。
- 真实多副本部署编排（P7）。

## 交付物

- 可启动的 API/Web；迁移可重复；设置中心生效。
- 管理员可配置 SUT/环境/页面/API/库/账号/代码仓；任意用户可建多 SUT 版本并进入空工作台。
- `contracts/openapi.yaml`、`events.md`、`db-schema.md`、`migrations-order.md`、`settings.md` 骨架。
- 单测/集成测试/演示脚本；`phase-00-handoff.md`。

## 依赖与前置

- 无前置阶段。
- 设计文档/参数默认已冻结；如发现缺口，停止报告，不自造字段。

## 验收/演示

1. 管理员登录 → 创建设置/资源 → 页面/API 清单可见。
2. 用户创建版本 → 绑定多个 SUT（各选环境、页面/API scope、库、账号、代码仓）→ 版本详情壳可打开。
3. 设置中心改一项非重启配置 → 生效；事件流可回放；迁移重复执行无副作用。
4. 测试全绿；contracts 骨架 frozen。

## Handoff 必须包含

- 新增表/迁移顺序、设置键、资源/版本绑定契约。
- 前端壳/Tab/路由、可复用组件。
- 未完成项（任务板/Agent/对话等）与 P1 输入。
- 已知风险（多 SUT 绑定、页面/API 去重、设置热加载）。

## 风险/约束

- 资源模型与后续阶段耦合最深；字段/scope 先按设计文档冻结，P1+ 不反复改。
- 不要恢复 0.1 的 System/Environment/AppModel 旧结构；按 0.2 SUT/环境/页面/API 模型。
- 4 项 env 是硬约束；新增配置必须进 DB 设置中心。
