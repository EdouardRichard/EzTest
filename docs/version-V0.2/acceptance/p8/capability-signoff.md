# EzTest 0.2 P8 能力矩阵签字（S11）

> 状态：**待 Lead 复跑 S11 并签字**（37 行检查表与证据指针已完备；本文件不替代 P8 S1–S6 运行记录）
> 日期：2026-11-02
> 分支：`feat/v0.2-p8`
> 依据：实施 spec [§11.5](../../../superpowers/specs/2026-11-02-eztest-v02-p8-hardening-acceptance-design.md)；[能力矩阵 §3](../../research/capability-matrix.md)；[P6 capability-parity](../p6/capability-parity.md)；[P7 acceptance](../p7/acceptance.md)；[openapi.yaml](../../contracts/openapi.yaml)
> 方法：37 行 A1–I2；`scripts/p8-capability.ps1` 用 admin 与普通用户双登录执行 HTTP/DB/页面文件/证据存在性检查；P8 不重跑的行标 `PASS(prior)` 并给出 P5/P6/P7 来源；P9 行只引用 `scripts/p7-acceptance.ps1` 证据，不在本脚本重实现。

## 1. 结论口径与执行限制

- `PASS`：该行由 P8 S11 入口 smoke 覆盖，且有明确的 HTTP/DB/页面文件判据；**不等于** P8 S1–S6 全旅程行为已在本文件中被重新执行。
- `PASS(prior)`：P8 S11 不重跑该能力行为，采用既往 P5/P6/P7 验收证据；来源见“既往证据”列及 §4。
- `FAIL`：S11 检查实际失败；本文件不出现 FAIL 行，但最终以 Lead 复跑结果为准。
- **未复跑前，`PASS` 行只是“已定义且可执行的 S11 判据”，不是运行结论**；Lead 复跑输出的 PASS 才构成 P8 证据。
- **当前执行限制**：本签字表在无 `p8` 库/API 的工作区生成，未执行 S11 全量矩阵，也未声称 S1–S6/S8–S10/S12–S13 的运行结果。Lead 必须在本机 p8 验收环境执行：
  `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-capability.ps1`
  输出日志：`.tmp/p8-capability/capability.txt`。若脚本退出码非 0（存在 FAIL），本表不得签字。
- S11 只做入口/抽查；真实 LLM、真实 Chrome、跨 SUT、长上下文、多副本、P9 行为分别由 P8 S1–S6、S8–S10 与 P7 S0–S12 的独立证据承担，本文件不把“可复现判据”写成“已执行结果”。
- 覆盖指针：S1–S6 全旅程、S8 多副本、S9/S10 长跑/硬化、S12 E2E、S13 静态/打包分别由 P8 验收脚本族承担；本文件不引用这些场景尚未产生的运行结果。

## 2. 37 行能力矩阵

| # | 能力 | 0.2 入口 | P8 证据（S11 判据） | 既往证据 | 结论 |
|---|---|---|---|---|---|
| A1 | 身份/用户/权限 | `/api/auth/*`、`/api/admin/users/*`；`/login` | S11 判据：admin+普通用户登录 200；`GET /api/auth/me` 200；普通用户 `GET /api/admin/users` 403 | [phase-00-handoff.md](../../phases/phase-00-handoff.md)；[P6 acceptance](../p6/acceptance.md) | PASS |
| A2 | LLM 配置/网关 | `/api/admin/llm/configs`、`/api/admin/llm/models`；`/settings` | S11 判据：configs=200 且 ≥1；models=200；LLM 设置面板存在 | [phase-00-handoff.md](../../phases/phase-00-handoff.md)；[P6 acceptance](../p6/acceptance.md) S0 真实 config test | PASS |
| A3 | 事件与 SSE | `/api/events`、`/api/stream`；版本详情 SSE | S11 判据：`/api/events?channel=system&limit=1`=200 且含 `snapshot_cursor` | [phase-00-handoff.md](../../phases/phase-00-handoff.md)；[P7 acceptance](../p7/acceptance.md) S8 跨实例 SSE | PASS |
| A4 | 文件/证据存储 | `/api/content/*`、`/api/versions/{id}/evidence`；执行 Tab 证据 | S11 判据：版本 evidence=200；`EvidenceViewer` 存在 | [phase-00-handoff.md](../../phases/phase-00-handoff.md)；[phase-03-handoff.md](../../phases/phase-03-handoff.md)；[phase-05-handoff.md](../../phases/phase-05-handoff.md) | PASS |
| A5 | 审计/幂等/限流 | `/api/admin/audit-logs`；`audit_log`；限流中间件 | S11 判据：admin audit=200；普通用户=403；`audit_log` 非空；429 由 P8 S10 另行验收 | [phase-00-handoff.md](../../phases/phase-00-handoff.md)；[phase-06-handoff.md](../../phases/phase-06-handoff.md) | PASS |
| A6 | 配置/迁移/部署 | `/api/health`、`/api/admin/settings`；`deploy/` | S11 判据：health=200 且 `0.2.0-p8`；settings=85；migrations=42；业务表=85；`deploy/README.md` 存在 | [phase-07-handoff.md](../../phases/phase-07-handoff.md)；[contracts/README.md](../../contracts/README.md)；`p8-contract-check.cjs` | PASS |
| B1 | 系统/环境/应用模型 | `/api/resources/suts`、`/api/resources/catalog`；`/resources` | S11 判据：suts=200 且 ≥1；catalog=200；资源页存在 | [phase-02-handoff.md](../../phases/phase-02-handoff.md) | PASS |
| B2 | 账号/Session/登录配方 | `/api/resources/suts/{id}/accounts`；资源-账号 | S11 判据：SUT accounts=200；SUT/DataAccess 面板存在 | [phase-02-handoff.md](../../phases/phase-02-handoff.md)；[P6 parity](../p6/capability-parity.md) B2 | PASS |
| B3 | 只读 DB 断言 | `/api/resources/suts/{id}/databases`；执行器 DB 断言 | S11 入口判据：SUT databases=200；**行为不在 P8 S11 重跑** | [phase-03-handoff.md](../../phases/phase-03-handoff.md)；[P6 parity](../p6/capability-parity.md) B3（P3 证据） | PASS(prior) |
| B4 | 本地 Git/代码智能 | `/api/versions/{id}/code/*`；代码与变更 Tab | S11 入口判据：version code repos=200；`CodeTab` 存在；**行为不在 P8 S11 重跑** | [phase-05-handoff.md](../../phases/phase-05-handoff.md)（13/13 PASS）；[P6 parity](../p6/capability-parity.md) B4 | PASS(prior) |
| C1 | 版本生命周期 | `/api/versions/*`；`/versions`、版本详情 | S11 判据：versions=200 且 ≥1；version detail=200；版本页存在 | [phase-01-handoff.md](../../phases/phase-01-handoff.md)；[phase-04-handoff.md](../../phases/phase-04-handoff.md) | PASS |
| C2 | 需求/澄清/关系/附件 | `/api/versions/{id}/requirements`、`/conversations`；需求与澄清 Tab | S11 判据：requirements=200；conversations=200；需求/聊天/侧栏/附件 UI 存在 | [phase-01-handoff.md](../../phases/phase-01-handoff.md) | PASS |
| C3 | 知识库（P9 D63） | `/api/knowledge/items`；`/knowledge` | S11 入口判据：admin knowledge=200；**行为引用 P7 S4/S5，不在 P8 重实现** | [p7-acceptance.ps1](../../../../scripts/p7-acceptance.ps1) S4/S5；[P7 acceptance](../p7/acceptance.md)；[P7 scenarios](../p7/scenarios.txt) | PASS(prior) |
| D1 | 用例脑图模型 | `/api/versions/{id}/design/graph`；用例设计 Tab | S11 判据：design graph=200；`DesignTab`/`DesignMindmap` 存在 | [phase-02-handoff.md](../../phases/phase-02-handoff.md)；[phase-05-handoff.md](../../phases/phase-05-handoff.md) | PASS |
| D2 | 设计生成/评审 | `/api/versions/{id}/design/overview`；DesignChat | S11 判据：design overview=200；`DesignChat` 存在 | [phase-02-handoff.md](../../phases/phase-02-handoff.md) | PASS |
| D3 | 视觉断言 | `/api/quality/visual-metrics`、`/api/versions/{id}/visual/overview`；VisualView | S11 入口判据：visual metrics/overview=200；`VisualView` 存在；**行为不在 P8 S11 重跑** | [phase-05-handoff.md](../../phases/phase-05-handoff.md)（13/13 PASS）；[P6 parity](../p6/capability-parity.md) D3 | PASS(prior) |
| D4 | 脑图导出 | `/api/versions/{id}/exports`；ExportCasesDialog | S11 判据：exports=200；导出对话框/下载 helper 存在 | [phase-02-handoff.md](../../phases/phase-02-handoff.md)；[phase-05-handoff.md](../../phases/phase-05-handoff.md) | PASS |
| E1 | 批次/调度/并发（波次） | `/api/versions/{id}/waves`、`/execution/overview`；执行 Tab | S11 判据：waves=200；execution overview=200；WaveList/ExecutionToolbar 存在 | [phase-03-handoff.md](../../phases/phase-03-handoff.md) | PASS |
| E2 | 执行/浏览器/证据 | `/execution/overview`、`/evidence`；ExecutionTab/EvidenceViewer | S11 判据：execution overview=200；evidence=200；执行/证据 UI 存在 | [phase-03-handoff.md](../../phases/phase-03-handoff.md)；[phase-05-handoff.md](../../phases/phase-05-handoff.md) | PASS |
| E3 | 暂停/恢复/检查点 | `/api/versions/{id}/waves/{waveId}/pause`、`resume`；PauseDialog | S11 入口判据：execution overview=200；`PauseDialog` 存在；暂停/恢复行为由 P8 S5/S9 另行验收 | [phase-03-handoff.md](../../phases/phase-03-handoff.md) | PASS |
| E4 | 失败分析/补全 | `/api/versions/{id}/issues`；failure/coverage agent；问题 Tab | S11 入口判据：version issues=200；`IssuesTab` 存在；**行为不在 P8 S11 重跑** | [phase-03-handoff.md](../../phases/phase-03-handoff.md)；[phase-04-handoff.md](../../phases/phase-04-handoff.md)；[P6 parity](../p6/capability-parity.md) E4 | PASS(prior) |
| E5 | 证据 GC（P9 D65） | `/api/admin/gc/runs`；设置 GC 面板 | S11 入口判据：admin GC runs=200；`GCPanel` 存在；**行为引用 P7 S6/S7，不在 P8 重实现** | [p7-acceptance.ps1](../../../../scripts/p7-acceptance.ps1) S6/S7；[P7 acceptance](../p7/acceptance.md)；[P7 scenarios](../p7/scenarios.txt) | PASS(prior) |
| F1 | 问题 | `/api/versions/{id}/issues`、`/api/issues`；问题 Tab | S11 判据：version issues=200；global issues=200；IssuesTab/GlobalIssuesPage 存在 | [phase-04-handoff.md](../../phases/phase-04-handoff.md)；[phase-06-handoff.md](../../phases/phase-06-handoff.md) | PASS |
| F2 | 报告（HTML/PDF） | `/api/versions/{id}/reports`、`/api/reports`；报告 Tab | S11 判据：version/global reports=200；ReportsTab 存在；**PDF 委托 P7 S1/S3 既往证据** | [phase-04-handoff.md](../../phases/phase-04-handoff.md)；[phase-06-handoff.md](../../phases/phase-06-handoff.md)；[P7 acceptance](../p7/acceptance.md) S1/S3 | PASS |
| F3 | 全局问题/报告中心 | `/api/issues`、`/api/reports`；`/issues`、`/reports` | S11 判据：global issues=200；global reports=200；两个页面文件存在 | [phase-06-handoff.md](../../phases/phase-06-handoff.md)；[P6 acceptance](../p6/acceptance.md) S5 | PASS |
| G1 | Agent profile/session/run/message | `/api/agent-center/runs`；DB `agent_run`；团队频道读模型 | S11 判据：agent runs=200；version team room=200 | [phase-01-handoff.md](../../phases/phase-01-handoff.md)；[phase-06-handoff.md](../../phases/phase-06-handoff.md) | PASS |
| G2 | Agent 中心/评测 | `/api/agent-center/catalog`、`/api/quality/*`；`/agents`、`/quality` | S11 入口判据：catalog=200 且 ≥1；quality overview=200；eval-runs=200；页面存在；**行为不在 P8 S11 重跑** | [phase-06-handoff.md](../../phases/phase-06-handoff.md)；[P6 acceptance](../p6/acceptance.md) S1/S4/S7；P6 截图 | PASS(prior) |
| G3 | 任务板/团队频道/上下文 | `work_item`/`team_room_post`/`team_room_digest`/`context_packet`；team-rooms | S11 判据：team room=200；四张表存在 | [phase-01-handoff.md](../../phases/phase-01-handoff.md)；[phase-06-handoff.md](../../phases/phase-06-handoff.md) | PASS |
| G4 | LLM 路由/预算/成本 | `/api/agents/profiles`、`/api/agent-center/catalog`；设置 | S11 判据：profiles=200 且 ≥1；catalog=200 | [phase-00-handoff.md](../../phases/phase-00-handoff.md)；[phase-01-handoff.md](../../phases/phase-01-handoff.md) | PASS |
| H1 | 应用壳/版本详情 | 8 项全局壳 + 8 Tab；AppShell/VersionDetailPage | S11 判据：AppShell/VersionDetailPage/tabs.ts 文件存在 | [phase-06-handoff.md](../../phases/phase-06-handoff.md)（8 shell / 8 Tab） | PASS |
| H2 | 澄清 UI | 需求与澄清 Tab | S11 判据：RequirementsTab/ChatPanel/RequirementSidebar/AttachmentUploader 存在 | [phase-01-handoff.md](../../phases/phase-01-handoff.md) | PASS |
| H3 | 脑图 UI | 用例设计 Tab | S11 判据：DesignTab/DesignMindmap/CaseDetailPanel 存在 | [phase-02-handoff.md](../../phases/phase-02-handoff.md) | PASS |
| H4 | 执行 UI | 执行 Tab | S11 判据：ExecutionTab/WaveList/EvidenceViewer/PauseDialog 存在 | [phase-03-handoff.md](../../phases/phase-03-handoff.md) | PASS |
| H5 | 问题/报告 UI | 问题/报告 Tab + 全局页 | S11 判据：IssuesTab/ReportsTab/GlobalIssuesPage/GlobalReportsPage 存在 | [phase-04-handoff.md](../../phases/phase-04-handoff.md)；[phase-06-handoff.md](../../phases/phase-06-handoff.md) | PASS |
| H6 | 搜索/待办/链路 | `/api/search`、`/api/todos`、`/exploration-chain`；CommandPalette/TodoDrawer/ChainTab | S11 判据：search=200；todos=200；chain=200；三个 UI 文件存在 | [phase-06-handoff.md](../../phases/phase-06-handoff.md)；[P6 acceptance](../p6/acceptance.md) S2/S3/S6 | PASS |
| I1 | 测试体系/fake LLM/E2E | Go 单测+集成、前端测试、Playwright E2E、契约门禁 | S11 入口判据：test.ps1、p8 contract gate、frontend/e2e 存在；**P8 S13/回归不在 S11 重跑** | [phase-06-handoff.md](../../phases/phase-06-handoff.md)；[phase-07-handoff.md](../../phases/phase-07-handoff.md) | PASS(prior) |
| I2 | 打包/CI/SLO/安全/备份 | `deploy/`、check-ci/security-audit/slo/backup 脚本 | S11 入口判据：`deploy/README.md` 与四个脚本存在；**P8 S13 静态/打包不在 S11 重跑** | [phase-07-handoff.md](../../phases/phase-07-handoff.md)；P5/P6 局部；Docker 未实跑 | PASS(prior) |

## 3. 未在 P8 S11 重跑的行及理由

1. **B3**：只读 DB 断言已在 P3 验收；S11 只验证目标库入口，不重新执行 SQL 断言（避免把验证变成对目标库的写操作）。
2. **B4**：代码智能（索引/diff/影响/锚点/事实复核）已在 P5 验收 13/13；S11 只验证版本 code repos 入口与 UI 文件。
3. **D3**：视觉断言真实 Chrome 基线/审批/actual/diff 在 P5 验收 13/13；S11 只验证 visual metrics/overview 入口。
4. **G2**：Agent 中心/评测在 P6 验收 S1/S4/S7 与 P6 截图；S11 只验证 catalog/quality/eval-runs 入口。
5. **C3 / E5（P9）**：知识库与证据 GC 是 P9 能力，行为证据只引用 `scripts/p7-acceptance.ps1` 的 S4/S5 与 S6/S7；S11 不重写/不重跑 P9。
6. **E4**：失败分析/补全链在 P3/P4 验收；S11 只验证问题入口与 UI 文件。
7. **I1 / I2**：测试/CI/SLO/安全/备份由 P6/P7 既有门禁与 P8 S13 承担；S11 只验证脚本/文档入口，不把“文件存在”冒充“CI 已运行”。

## 4. 签字

- **Lead 代用户签字（待用户审阅 handoff）**：____________________
- 签字日期：____________________
- 说明：Lead 在 p8 验收环境复跑 `scripts/p8-capability.ps1` 且无 FAIL 后签字；用户审阅 handoff 后可要求复验。本文件未声称 S1–S6/S8–S10/S12–S13 已运行；这些场景的结论以各自 acceptance 文档为准。

## 5. Lead 复跑结果（2026-11-02）

- 命令：`powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p8-capability.ps1`
- 环境：eztest_v02_p8，API http://127.0.0.1:13333，真实 P8 二进制
- 结果：**PASS=28 / PASS(prior)=9 / FAIL=0 / total=37**，退出码 0
- 证据：`.tmp/p8-capability/capability.txt`、`.tmp/p8-acceptance-run/capability3-console.log`
- 说明：B4 本次因一次性 P8 数据库没有 code repository 行，只断言 UI 文件与 P5 先验证据，按 PASS(prior) 计；未伪造 code-intel 行为通过。
- 签字：Lead 代用户签字（2026-11-02）。用户审阅 handoff 后可要求复验。