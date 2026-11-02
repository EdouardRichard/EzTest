# P8 已知限制与用户批准的偏离

> 日期：2026-11-02；分支：`feat/v0.2-p8`（已合回 `feat/v0.2`）
> 依据：`docs/version-V0.2/phases/phase-08-hardening-acceptance.md`、P8 spec `docs/superpowers/specs/2026-11-02-eztest-v02-p8-hardening-acceptance-design.md`、用户 2026-11-02 裁定。
> 本文件只记录事实与处置，不替代设计文档。

## 用户批准的偏离

| # | 偏离 | 用户裁定 | 影响/处置 |
|---|---|---|---|
| D1 | phase-08 仍列 **SLO/性能结果**；P8 不测量 | 2026-11-02：“不用测，跳过这项。” | 无 SLO 报告/阈值；handoff 记录为已知偏离；架构 §13 “SLO 后置/按需”不受影响 |
| D2 | P8 总计划列 **安全审计**；P8 不做 | 2026-11-02：“不做安全审计。” | 不运行 0.1 `security-audit.ps1`；不产出安全审计报告；仅遵守 secret 不入库/不入提交的既有约束 |
| D3 | phase-08 原列 **fake-LLM 回归**；P8 不做 | 用户确认 phase-08 未提交修改为 P8 范围基线 | 验收全部真实 LLM/Chrome；无确定性 fake-LLM 门禁（P6 U4 已裁定） |
| D4 | phase-08 原列 **性能/SLO**；P8 不做性能基准 | 同上 | 不设性能阈值；验收记录真实耗时但不作为门禁 |
| D5 | 计划要求 **Docker 实跑**；本机无 Docker | 2026-11-02：静态校验 + 文档，明确记录未实跑 | `deploy/` 只做 compose/Dockerfile 静态校验；不宣称已运行 |
| D6 | S9 长执行/`/p8/large`/checkpoint/head+ref、pinned facts、resilience teardown（L16–L18） | 2026-11-02：用户选择 **B（批准豁免，先发布版本；之后回到 A 继续补 S9）** | v0.2 发布签核接受 S9 长执行为 partial；L16–L18 明确记录；后续在独立分支继续补齐，不改变本次发布 |

## 产品/工程已知限制

| # | 限制 | 影响 | 处置 |
|---|---|---|---|
| L1 | 限流默认 `security.rate_limit_per_minute=0`（关闭） | 默认部署不限制请求；管理员需显式开启 | 文档写明总速率=副本数×限额；P8 验收临时设 30 验证 429 后恢复 |
| L2 | 限流为**实例级**内存令牌桶，不是 DB/Redis 共享额度 | 多副本总速率=副本数×限额，实例重启后桶重置 | 架构 §12.4 允许“按实例或 DB 额度”；文档明确 |
| L3 | 事件/审计清理为 leader 日循环（`retention.gc_interval_minutes`） | 删除有最长约 1 天延迟；旧游标回放依赖既有 `stream.reset` | 验收覆盖旧删新留/游标语义；保留期可配 |
| L4 | 多执行器派发按 `owner_executor_id` 路由；历史行 `executor_id IS NULL` 回退主端点 | 升级前的旧 context 仍只会在主端点清理 | 迁移只加列；旧行兼容回退；单端点模式完全回归 |
| L5 | 事件清理后，超过保留期的 `Last-Event-ID` 回放会得到 `stream.reset` | 客户端需重新取快照 | 既有契约语义；验收覆盖 |
| L6 | Docker Compose 未在本机实际构建/启动 | 镜像/网络/卷只经静态校验 | `deploy/README.md` 与 handoff 明确“未实跑”；不宣称已验证 |
| L7 | 0.1 文档/脚本仍留在仓库 | 根 `docs/*.md` 是 0.1 历史；0.1 脚本（`acceptance.ps1`/`slo.ps1`/`security-audit.ps1` 等）不再被 CI/P8 引用 | 根 README 升级到 0.2；0.1 文档加归档横幅；0.1 脚本保留只读 |
| L8 | SLO/安全审计/fake-LLM/性能基准不在 P8 交付 | 与 phase-08 原文/总计划有偏离 | 见 D1–D4；用户已批准，handoff 记录；如需补做另立阶段 |
| L9 | 真实 LLM 输出非确定性 | 澄清/设计验收可能因模型输出重试 | 验收最多重试 2 次；失败记产品/提示词缺陷，不用 fake 数据 |
| L10 | 真实验收依赖本机 Chrome/DeepSeek/便携 PostgreSQL | 换机需按 runbook 准备 | `acceptance/p8/runbook.md` 记录环境/命令/端口 |
| L11 | 多轮澄清可能让 Curator 重复生成同题 requirement draft | 若未去重，可能产生重复 draft，阻塞收尾 | P8 验收改为 3 轮（首轮创建、后两轮只提问/总结）；若仍重复，作为 P2 产品风险记录；当前未改 Curator 去重逻辑 |
| L12 | 视觉断言可能结束为 `inconclusive`，P8 没有 waiver 接口 | 若自动确认所有 design-gap 视觉用例，closure 会因 inconclusive 阻塞 | 验收只确认初始用例与有执行价值的 draft，design-gap 视觉 draft 保持 draft；closure 只评估 current+confirmed。后续阶段应补显式 waiver |
| L13 | `requirement.close` 依赖 work-item 重试预算（当前 10 次，即上限） | 若同需求的设计/分析任务持续超过约 3 分钟，close 任务仍可能 dead | 验收覆盖 `design.gap` 排空后成功关闭；更长阻塞需人工/后续阶段调重试模型 |
| L14 | 真实 LLM 设计输出可能是自由文本步骤，而非可执行 action | 个别用例会在编译阶段 automation/inconclusive | P8 验收对跨系统用例失败做公开 manual-intent 重试；E2E 将无法执行的 UI 变更记为显式 gap，不伪造通过 |
| L15 | 一次性 P7→P8 数据库常无 code_repository 行，或报告内容对象缺失 | B4 只能按 prior 证据、报告下载测试可能只能 skip | Capability B4 记 PASS(prior) 并注明原因；E2E 发现报告字节不可读时 `test.skip`，不记 PASS |
| L16 | S9 pinned facts 在最终 long-context run 中为 0 | 上下文包“保留 pinned facts”未被实际触发 | 用户已批准豁免（D6）；文档如实记录 `pinned_facts=0`；后续 A 阶段补显式 pin 用例 |
| L17 | S9/S10 长执行在最终 run 使用 P3 fallback（6 个执行用例，非 spec 的 12 用例/2 波） | 12 用例、`/p8/large?bytes=20000` head+ref、checkpoint 恢复仍无 P8 证据 | 用户已批准豁免（D6）；不得作为 12 用例通过；后续 A 阶段补 fixture 与确定性 12 用例验收 |
| L18 | Resilience 脚本在 P3 fallback 后 teardown 挂起，未打印自身 exit code | S9/S10 证据来自运行日志与数据库，不是脚本 exit 0 | 用户已批准豁免（D6）；后续 A 阶段修复 teardown 并重跑完整 exit-0 |

## 未纳入 0.3 前必须知悉的边界

- 多用户协作/复杂 RBAC、非 PostgreSQL 目标库断言、外部工单/CI/IM/Webhook、多租户、生产级 TLS/密钥托管、自动扩缩容、Redis/NATS/对象存储：仍不在 0.2 范围。
- 0.1 数据不迁移；0.2 使用新库。
- 视觉/代码智能/质量/搜索/Agent 中心能力沿用 P5/P6 证据与入口 smoke；P8 不重写这些领域。

## 变更方式

任何限制的解除必须在后续阶段 spec/决策记录中立项，并同步 contracts、迁移、测试与 handoff；P8 不自行扩大范围。
