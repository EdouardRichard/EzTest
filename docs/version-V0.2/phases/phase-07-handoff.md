# Phase 07 Handoff — P9 五项（P7）

> 状态：**completed（全部门禁/验收通过；契约 `frozen: P7`）**
> 日期：2026-11-02
> 阶段：P7（`docs/version-V0.2/phases/phase-07-p9.md`）
> 实施分支：`feat/v0.2-p7`（从 `feat/v0.2` @`a1bd99b` 拉出）→ 合回 `feat/v0.2`（merge 提交见 §7）
> 实施 spec：[2026-11-01-eztest-v02-p7-p9-design.md](../../superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md)
> 实施 plan：[2026-11-01-eztest-v02-p7-p9.md](../../superpowers/plans/2026-11-01-eztest-v02-p7-p9.md)
> 验收证据：[acceptance/p7/](../acceptance/p7/)

## 0. 前置、读取依据与流程

- 按 `implementation-phase-plan.md`：P7 → phase-07；PREV_HANDOFF=`phase-06-handoff.md`；HANDOFF=本文件。
- 实际读取：`Agent.md`、0.2 README/architecture（§11–§13、§18）/high-level（§9–§11）/decision-log/capability-matrix/open-decisions/implementation-phase-plan、phase-06/07、全 contracts、`ui/mock.html`；代码现状审计（迁移 33、设置 72、delivery/report/issue、eventing Hub、agent lease、execution registry、storage ledger）。
- 流程：brainstorming（范围 A + 分支 `feat/v0.2-p7` + 验收复用真库/真 LLM/真 Chrome + 6 项设计缺口裁定）→ spec 用户批准 → writing-plans（22 任务/5 波）→ 用户批准“子代理并行无人值守” → W0 串行底座 → W1/W2 并行实现 → Lead 装配 → 真实验收 → 最终评审 + 修复波 → 复跑。
- 用户裁定（2026-11-01）：P7-D-01…P7-D-10（见 spec §3），含 `sut.family`、独立 renderer、单模板 v1/v2、`gc_run/gc_tombstone`、`api_instance`+advisory-lock leader、admin 权限、`ops` 团队、复发读时重定向、多端点 pull + 最小 push、0.1 导出脚本后置。

## 1. 完成范围

- **D59**：report 模板 v1（字节不变）/v2（+附录）+ `report.section_appendix`；PDF 由独立 `renderer/` 进程（Playwright + Chrome channel，Bearer，2 MiB/60s/并发 2）生成；版本域 work_item `report.render_pdf`（team `ops`）+ 租约/心跳/重试/失败状态；`pdf_status/pdf_error/pdf_attempts/pdf_rendered_at/pdf_ref`；`GET .../revisions/{revision}/pdf`；事件/审计；旧 revision HTML/as_of/指纹不可变；历史 revision PDF 补齐。
- **D63**：新上下文 `backend/internal/knowledge/`（domain/application/postgres/httpapi）；`global`/`system` scope（`system_key = sut.family`）、draft→approved/rejected→revoked→approved、管理员审核、`source_stale` 过滤、PG 全文 + 标签、预算/条数注入、`knowledge_usage`、`version_pack.knowledge[]` + `[K-id]` 引用、`knowledge.propose` 工具（澄清/设计 5 个 profile）、全局 `/knowledge` 页面。
- **D65**：`gc_run`/`gc_tombstone`；四类候选（unreferenced/temp/superseded_export/orphan_file）+ 悬空 ref 清理；引用保护（report revision HTML/PDF、活附件/证据、当前导出、宽限 24h、共享对象）；dry-run 默认、apply 需 `confirm`、单活跃 run + 失联租约回收（900s）；tombstone/审计/事件；`POST/GET /api/admin/gc/runs`；设置页 GC 面板。
- **D67**：`orchestration` 上下文：`api_instance` 注册/心跳/node_id 分配/离线/排空、PG advisory-lock leader（reconciler/scheduler/poller/GC 仅 leader；agent/codeintel/render worker 支持 drain gate）；`GET /api/health/ready`；`GET /api/admin/instances` + drain/activate；跨实例 SSE（`pg_notify {channel,seq}` + 每实例 LISTEN → 本地 Hub，回放不变）；执行器多端点 pull + push register/heartbeat + capacity/offline；`deploy/` 双 api + renderer + 共享卷；`scripts/p7-multi.ps1`。
- **问题**：P4 重开/合并保留；合并后复发沿链重定向（refs role=`recurrence`、`recurrence_count/last_recurred_at`、history/事件/审计、源不变、断链回退）；`merged_chain`；旧报告 as-of 不变；UI 复发/合并链/重开。
- **横切**：SUT `family`（迁移+API+UI+注入）；报告详情节只返回当前 revision refs（去重）；契约冻结；验收/E2E 脚本与截图。

## 2. 契约变更（全部 `frozen: P7`）

| 类型 | 变更 |
|---|---|
| openapi | `0.2.0-p7`/`x-frozen: P7`；**162 paths / 200 ops / 264 schemas**；新增 18 ops / 15 path items（知识 8、GC 3、实例 3、ready 1、PDF 1、内部执行器 2）；19 新 schema；`pdf_available` 扩宽 bool + PDF 字段；`IssueView` 复发字段；SUT `family`；tags `Knowledge`/`Admin`/`Internal` |
| 事件 | **12**：`report.pdf_ready/pdf_failed`、`knowledge.proposed/approved/rejected/revoked`、`gc.completed`、`instance.registered/drained/activated/offline`、`issue.recurred`；NOTIFY payload `{channel,seq}`（event PK 无全局 id） |
| 表 | 新 6：`knowledge_item/revision/usage`、`gc_run`、`gc_tombstone`、`api_instance`；扩宽 4：`sut.family`、`delivery_report_revision`(4 列)、`delivery_issue`(2 列)、`work_item.team`(+`ops`) |
| 迁移 | **40**：#34 knowledge、#35 storage gc、#36 delivery report pdf、#37 orchestration instance、#38 resource sut family、#39 delivery issue recurrence、#40 agent ops team；`order.txt` 40 |
| 设置 | **83**（72+11）：`report.section_appendix/render_max_attempts/render_lease_seconds/render_worker_url/render_token`、`knowledge.injection_max_items`、`retention.gc_interval_minutes/gc_batch_size`、`instance.heartbeat_seconds/offline_seconds`、`execution.executor_urls`；`report.template_version` 新库默认 `"2"`（既有库保持 `"1"`） |
| 任务/工具 | `report.render_pdf`（team `ops`）；工具 `knowledge.propose`（5/14 profiles） |
| 协议/门禁 | `executor-protocol.md` §12 push/多端点/派发边界；`scripts/p7-contract-check.cjs`；P2–P6 检查改前向兼容 |

## 3. 测试与验收证据

- `scripts/test.ps1` 最终树 **exit 0**（Go unit + integration + frontend lint/typecheck/test 808 tests）。
- `go test -count=1 ./...` / `-tags=integration ./...`（含 cmd/server/delivery/storage/orchestration/knowledge）全 PASS。
- executor `node --test` **111/111**；renderer `node --test` **23/23**（含真实 Chrome PDF）。
- 契约门禁：`p7-contract-check.cjs` + P2–P6 全 PASS；compose 校验 PASS。
- **真实 LLM + Chrome API/DB 验收**：`scripts/p7-acceptance.ps1` **S0–S12 全 PASS，279s**；克隆 `eztest_v02_p6`→`eztest_v02_p7`（40 迁移/83 设置）。证据：`acceptance/p7/acceptance.md`、`scenarios.txt`。
  - 关键：S1 真实 PDF 211002B `%PDF-` + ledger sha、revision 五字段/HTML 字节不变；S2 死 renderer→failed+事件→恢复后成功；S3 附录/模板 2；S4/S5 真实 run 的 `context_packet` 含 `knowledge` 与 `[K-`、usage 行、revoke 后排除、scope=system 匹配 family；S6/S7 dry-run 报告 vs apply 精确删除+tombstone/审计/事件+保护完好；S8 跨实例 SSE + Last-Event-ID；S9 双副本 drain/leader 接管/无重复执行/迁移一次；S10 离线→interrupted(executor_lost)+epoch++；S11 合并后跨波次复发重定向 count 1→2、源不变；S12 旧 revision bytes/as_of 不变、新 revision 反映现状。
- **浏览器 E2E**：`scripts/p7-e2e.ps1` **5 passed**；**21 张截图** `acceptance/p7/screenshots/p7-e2e-01..21`；`acceptance/p7/e2e.md`。
- 多副本演练：`scripts/p7-multi.ps1`（`-ValidateOnly` 通过；双副本 drain/leader 断言在 S9 内覆盖）。

## 4. 已知缺口/偏差

1. P7 评审 1 Critical/12 Important 已修（渲染租约/心跳、ensurer 饥饿、GC 卡 running、worker drain gate、draining 实例 node 泄漏、bootstrap/node0 ID 冲突、PDF fence/重复 ref、报告详情 ref 去重、派发契约措辞、compose renderer token）；新增测试随后端套件。
2. **延期（记录 P8）**：跨实例设置缓存失效（`retention.gc_dry_run` 等热更只在更新进程生效）；in-flight drain 计数；知识注入的版本/需求标题 FTS seam（当前 query 为空、按 scope+id）；执行器多端点**派发**仍限主端点（多端点注册/心跳/列表/容量已就绪，per-executor 路由 P8）；事件日志/审计清理；限流。
3. 验收代理口径：S9 “drain 后不再认领”以 readiness/status 代理（无 claim 窗口插桩）；S10 重派用合成 running case_run（未跑真实 Chrome 执行）；S12 收尾谓词用版本状态+报告 published 代理；S2 重试为“恢复+重新生成”而非同一 work_item 重试。
4. 产品 bug 修复记录：P7-BUG-01 通用 agent worker 认领 `report.render_pdf` → 新增 `ops` 团队/迁移 #40；P7-E2E-BUG-01 报告详情重复 refs → 当前 revision 去重。
5. 其他偏差：API 版本串仍 `0.2.0-p5`（P6 起遗留）；PDF 上限 ledger 10 MiB vs renderer client 20 MiB（>10MiB 终止失败，P8 对齐）；executor push 心跳用 executor 环境变量而非 DB 设置；新增 compose 需一次性设置 `report.render_worker_url=http://renderer:13337`（默认值对单机可用）；0.1 `scripts/acceptance.ps1 -StaticOnly` CI 静态门禁仍是 0.1 树（P6 基线，P8 处置）。
6. 设计文档未改；`D59/D63/D65/D67` 无独立决策行，本阶段引用 architecture §12 / HLD §9 + OD-2/3/4/11。

## 5. P8 输入

- 契约：`frozen: P7` 全量；P8 只消费/追加。
- 表/API：见 §2；验收数据 `eztest_v02_p7`（可复用）。
- 脚本：`scripts/p7-acceptance.ps1`、`p7-e2e.ps1`、`p7-multi.ps1`、`p7-contract-check.cjs`、`dev-renderer.ps1`。
- 必须复核：跨实例设置失效、in-flight drain、事件/审计清理、多执行器派发、知识 FTS 注入、10MiB PDF 上限、0.1 CI 静态门禁、真实 Chrome 的离线重派与收尾谓词全链路。
- 风险：GC 误删（已有保护/宽限/确认，P8 复核保留期与保护枚举）、多副本 leader 切换窗口（≤2s 无 leader，不重复执行）、报告/问题一致性（as-of 不变已测）。

## 6. 风险

| 风险 | 现状/对策 |
|---|---|
| GC 误删 | dry-run 默认、24h 宽限、引用保护、tombstone/审计、apply 二次确认；P8 复核保护枚举 |
| PDF 渲染/重试 | 独立进程、超时/重试上限、失败不阻塞 HTML/收尾、租约心跳一致 |
| 多副本重复执行 | leader gating + lease/fencing + drain gate；S9 断言计数与迁移一次 |
| 雪花/实例 ID | 注册表分配 node + 冲突重试；首启同毫秒极端并发仍有窗口（P8 可加序列） |
| 知识污染 | 默认审核、AI 仅草案、revoke 生效、来源/审计可追溯 |
| 旧模板 | 既有库保持 v1，新库 v2；旧 revision 永不重渲染 |

## 7. 停止声明

P7 已交付并合回 `feat/v0.2`（merge 提交：本文件所在 commit 的父提交，见 `git log --merges`）：测试/契约/真实 LLM+Chrome 验收/E2E 全绿，`frozen: P7`，本 handoff 完成。**不自动进入 P8**，等待用户验收/指示。
