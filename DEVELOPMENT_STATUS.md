# EzTest 开发状态（0.2 / P8）

> 状态：**0.2 P8 硬化与验收已完成**（2026-11-02，`feat/v0.2-p8` 已合回 `feat/v0.2`，merge `f5c5801`）。
> 产品定位：AI 驱动、半自主、端到端测试系统；0.2 为完整替代版（0.1 P0–P8 全功能对齐 + P9 五项）。
> 设计权威：[docs/version-V0.2/README.md](docs/version-V0.2/README.md) 与
> [docs/version-V0.2/contracts/README.md](docs/version-V0.2/contracts/README.md)。
> 0.1 长历史已归档到 [docs/version-V0.1/DEVELOPMENT_STATUS.md](docs/version-V0.1/DEVELOPMENT_STATUS.md)。

## 0. 验证状态（先读）

- **P8 验收已 PASS**：证据索引 [docs/version-V0.2/acceptance/p8/](docs/version-V0.2/acceptance/p8/)。
  全旅程 S0–S6（568s）、韧性 S9/S10、多副本 S8（72 PASS/0 FAIL）、37 行能力矩阵
  （28 PASS/9 PASS(prior)/0 FAIL）、浏览器 E2E（10 passed/1 显式数据 gap）、静态门 18/18 均通过；
  证据与限制见 [phase-08-handoff](docs/version-V0.2/phases/phase-08-handoff.md)。
- **已记录证据**：P6（`acceptance/p6/`，真实 LLM + 真实 Chrome，S0–S9 PASS，9 张截图）与 P7
  （`acceptance/p7/`，13/13 场景、156 checks、21 张 E2E 截图，覆盖 P9 五项）。
- **Docker 未实跑**：`deploy/` 的 Compose/Dockerfile 已交付并静态校验；本机未安装 Docker，从未构建
  或启动。
- **SLO 与安全审计按用户裁定跳过**（P8-D-03 / P8-D-04）：不产出 SLO 报告、不运行 0.1
  `security-audit.ps1`，文档不作相关宣称。
- P8 的 fake-LLM 确定性回归与性能基准按用户裁定不做（P8-D-01）。

## 1. P0–P8 阶段

| 阶段 | 主题 | 主要交付 | 证据 |
|---|---|---|---|
| P0 | 平台底座与契约 | 4 项启动 env、设置中心、事件/审计/SSE、内容存储、统一信封/错误码、迁移与 OpenAPI 骨架 | [phase-00-handoff](docs/version-V0.2/phases/phase-00-handoff.md) |
| P1 | 控制面 + 澄清 | 任务板/reconciler/租约、团队运行时、上下文包、澄清对话与需求版本化、人机确认 | [phase-01-handoff](docs/version-V0.2/phases/phase-01-handoff.md) |
| P2 | 设计 | 设计对话、场景/用例/步骤/断言、用例版本与确认、脑图与自审、数据前置 | [phase-02-handoff](docs/version-V0.2/phases/phase-02-handoff.md) |
| P3 | 执行 | 执行意图/波次、Playwright 执行器与协议、多 SUT 上下文、证据、暂停/恢复、人机请求 | [phase-03-handoff](docs/version-V0.2/phases/phase-03-handoff.md) |
| P4 | 交付 + 链路 | 问题生命周期、报告 revision/HTML、探索链路、需求闭环、版本重开 | [phase-04-handoff](docs/version-V0.2/phases/phase-04-handoff.md) |
| P5 | 代码智能 + 视觉断言 | 只读本地 Git 索引/树/diff/影响/锚点/事实复核、视觉断言与 diff 证据、质量指标 | [phase-05-handoff](docs/version-V0.2/phases/phase-05-handoff.md) |
| P6 | 全功能补齐 | 全局搜索/待办、质量中心/评测、Agent 中心、全局问题/报告、链路 v2、提示词版本化 | [phase-06-handoff](docs/version-V0.2/phases/phase-06-handoff.md) |
| P7 | P9 五项 | 报告 HTML/PDF 与模板、跨版本知识、统一 GC、多实例/滚动升级、问题重开/合并/跨波次去重 | [phase-07-handoff](docs/version-V0.2/phases/phase-07-handoff.md)、[acceptance/p7](docs/version-V0.2/acceptance/p7/) |
| P8 | 硬化与验收 | H1–H11 硬化（跨实例设置失效、in-flight 计数、知识 FTS、per-executor 派发、PDF 上限、版本串、动态 E2E seed、雪花/实例 ID、CI 去 0.1 化、事件/审计保留、限流）、P8 契约冻结、全量验收、文档 0.2 化 | [phase-08-handoff](docs/version-V0.2/phases/phase-08-handoff.md)、[acceptance/p8](docs/version-V0.2/acceptance/p8/) |

## 2. 契约与规模计数（P8 冻结）

| 项 | 计数 |
|---|---|
| OpenAPI | `0.2.0-p8` / `x-frozen: P8`；**162 paths / 200 operations / 264 schemas** |
| 业务表 | **85**（P8 不新增表） |
| 迁移 | **42**（P8 追加 #41 `api_instance.active_requests/active_work`、#42 `case_run_sut_context.executor_id`） |
| 数据库设置 | **85**（P8 新增 `security.rate_limit_per_minute=0`、`retention.event_days=90`） |
| 事件类型 | **118**（P8 不新增类型；新增审计 action `retention.cleanup`） |
| 任务类型 | **19** |
| Agent profile | **14** |
| 限界上下文 | **20** |
| 执行器线协议 | **v1.2**（P8 只改派发路由，无 action 变化） |

契约门禁：`node scripts/p8-contract-check.cjs`（P2–P7 检查已改为前向兼容）。

## 3. 测试面

| 项 | 计数/状态 |
|---|---|
| Go 测试 | **321 个 `*_test.go` / 103 个包**（P8 起点 316/102；H1–H11 新增测试随分支加入） |
| 前端 Vitest | **105 个测试文件** |
| executor / renderer | 111 / 23（P7 收口记录；P8 未改其协议与实现） |
| 统一测试入口 | `pwsh scripts/test.ps1`（Go unit + integration、前端 lint/typecheck/test） |
| 契约检查 | `node scripts/p8-contract-check.cjs`；P2–P7 契约脚本前向兼容 |
| 静态检查 | `scripts/p8-static-check.ps1`（P8 收口；校验 4 env、文档、契约计数、Compose、无 0.1 门禁引用、无 secret） |

## 4. 验收证据

| 阶段 | 证据路径 | 摘要 |
|---|---|---|
| P6 | [docs/version-V0.2/acceptance/p6/](docs/version-V0.2/acceptance/p6/) | S0–S9 PASS；质量/搜索/待办/Agent 中心/全局中心/链路 v2；真实 LLM 评测；9 张截图 |
| P7 | [docs/version-V0.2/acceptance/p7/](docs/version-V0.2/acceptance/p7/) | S0–S12 共 13 场景、156 checks PASS；PDF/知识/GC/多副本/SSE/离线重派/问题复发；5/5 E2E、21 张截图 |
| P8 | [docs/version-V0.2/acceptance/p8/](docs/version-V0.2/acceptance/p8/) | 全旅程/韧性/多副本/能力矩阵/E2E/静态门 PASS；限制见 handoff/known-limitations |

P8 验收入口（详见 runbook）：`p7-acceptance.ps1`（P9 回归）→ 克隆 `eztest_v02_p7` 为
`eztest_v02_p8` → `p8-acceptance.ps1`（S0–S6）→ `p8-resilience.ps1`（S9–S10）→ `p8-multi.ps1`
（S8）→ `p8-capability.ps1`（S11）→ `p8-e2e.ps1`（S12）→ `p8-static-check.ps1`（S13）。环境为真实
LLM、真实 Chrome、双 SUT、双 API 副本与共享内容目录。

## 5. 已知限制

1. **P8 最终验收结果尚未记录**：本文件只记录入口与已有 P6/P7 证据；P8 PASS/FAIL 以
   `acceptance/p8/` 的最终文档为准。
2. **Docker 未实跑**：Compose/Dockerfile 只做静态校验（`frontend/scripts/validate-compose.mjs`）；
   首次在真实 Docker 主机执行前需复核网络、卷、renderer URL/token 与镜像构建。
3. **SLO 未测量、安全审计未执行**：按用户裁定跳过；不承诺 SLO 阈值或生产级安全加固。
4. **多副本边界**：API 多副本通过 PostgreSQL 租约/advisory lock 协调，要求所有副本共享同一内容
   文件系统；不做自动扩缩容、跨区域 HA 或 Redis 共享限流桶。限流为每实例额度，总速率 = 副本数 ×
   限额（默认 0 关闭）。
5. **数据迁移**：0.1 数据不做原地迁移，0.2 使用新库；0.1 文档与脚本仅归档参考。
6. **外部依赖**：真实执行需要可用 Chrome/Chromium；真实 AI 路径需要 OpenAI 兼容 LLM；PDF 需要
   `renderer/` 进程。没有 LLM 时确定性路径仍工作，AI 路径返回可重试错误。
7. **保留与 GC**：事件保留默认 90 天、审计默认 90 天；GC 默认 dry-run，apply 需显式确认；没有
   冷存储/归档导出。
8. **验收环境**：验收脚本以 Windows PowerShell 为主，使用本机 PostgreSQL/Chrome；跨平台复跑需
   等价环境。

## 6. 文档入口

- 产品 README：[README.md](README.md) / [README.zh-CN.md](README.zh-CN.md)
- 0.2 设计索引：[docs/version-V0.2/README.md](docs/version-V0.2/README.md)
- 0.2 契约：[docs/version-V0.2/contracts/](docs/version-V0.2/contracts/)
- 部署/Compose：[deploy/README.md](deploy/README.md)
- P8 runbook：[docs/version-V0.2/acceptance/p8/runbook.md](docs/version-V0.2/acceptance/p8/runbook.md)
- 0.1 归档：[docs/version-V0.1/README.md](docs/version-V0.1/README.md)

## 7. 许可证

Apache License 2.0，见 [LICENSE](LICENSE)。
