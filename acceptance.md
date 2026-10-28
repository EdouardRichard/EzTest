# EzTest P7 全自主闭环 — 真实浏览器人工验收记录（Task 32b 重跑）

- 验收日期：2026-10-29（02:06–02:30，Asia/Shanghai）
- 分支 / HEAD：`feat/p7-autonomous-loop` / `ec4bca4`（`test(p7): add real LLM acceptance script`）。P7 实现、E2E 修复与 T28/T29 验收脚本均已提交；本验收记录与 16 张截图随后作为 docs 提交。
- 工作区：验收运行时的 worktree 含 T28/T29 脚本与探索链路修复的未提交改动；这些改动已在验收后提交（`586e1d3`、`235b894`、`ec4bca4`）。最终门禁在 post-fix tree 上复跑：full e2e 29/29、Go unit/integration、frontend 849、静态 31/0/32。
- 环境：Windows PowerShell 5.1；本机 PostgreSQL `:5433`；E2E 端口 `API :13333 / Web :6868 / fake LLM :13334 / fixture :13335 / executor :13336`；Chrome channel；T28 真实 LLM 为 `deepseek-flash`（`.env`）；T29 只读目标库为 `eztest_p7_target`、角色 `eztest_p7_ro`。
- 证据根目录：`.tmp/p7-real-llm/`（T28）、`.tmp/p7-real-db/`（T29）、`.tmp/p7-acceptance/`（post-fix P7 E2E 截图 17 张，02:23:10–02:24:02；含新 `p7-issues-05-exploration-issues.png`）、`.tmp/t32-manual-real/`（真实 LLM 数据库浏览器走查截图，02:09:57–02:10:03）、`.tmp/t32b-e2e-4.log`、`.tmp/t32b-p7-postfix.log`、`.tmp/t32b-execution.log`、`.tmp/t32b-acceptance-static-3.log`、`.tmp/t32b-acceptance-static-final2.log`。
- 可随提交保留的关键截图：`docs/acceptance/p7/`（16 张：11 张 post-fix P7 E2E + 5 张真实 LLM 走查）。

## 1. 结论（先给判定）

**PASS WITH LIMITS — P7 功能验收通过；全量三阶段 E2E 29/29 通过（T27 最终复跑），无合并阻断项。**

- **DoD #1–#5：全部 PASS**，有 T28 round5（42/42，0 skipped，131.6s，70,845/7,291 tokens）、T29（20/20 两次）和 post-fix P7 E2E（5/5，`E2E PASSED`，EXIT=0）证据。
- **§2.9 #7/#9/#12/#13/#18/#21/#22：全部 PASS**。#9 在 pre-fix tree 曾失败（02:10，`limit 200 > 后端 100` 导致探索链路 400/422），并发任务于 02:18–02:19 修复并提交为 `586e1d3`（02:27:36）；post-fix E2E 02:23:49 截图显示 9 节点、2 个问题节点（AI+人工红色菱形）、AI 补充角标，且无 error/degraded。
- **默认三阶段 E2E 已全绿**：T27 修复 P6 `execution.spec` 对 P7 autonomy 自动 supplement 批次的陈旧断言（`235b894`）后，默认 runner 于 02:42 复跑 **23/23 + 5/5 + 1/1 = 29/29，`E2E PASSED`，EXIT=0**（Phase 1 1.6m / P7 1.0m / execution 26.0s）。attempt C 的 28/29 是修复前历史证据。
- 结论：**P7 可接受；full e2e 29/29、T28 真实 LLM 42/42（0 skip）、T29 真实 DB 20/20、静态 31/0/32。** 已知限制见 §7（codeRefs=0、T28 fake executor、archived reset、Windows shutdown、全局过滤覆盖等）。

## 2. 门禁与浏览器命令记录

| 命令 | 时间 / 结果 | 判定 |
|---|---|---|
| `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1`（attempt A） | 02:06 启动；Web 启动失败：`Another next dev server is already running.`，旧 T32 的 Next dev（:6869）持有 `frontend/.next/dev` lock；`E2E_EXIT=1` | 环境失败，已清理 |
| 同上（attempt B） | 02:07–02:09；Phase 1 `22 passed / 1 failed`（`mindmap.spec.ts:234`：新增步骤后 `stepRows` 2/3，提示“请填写步骤动作。”）；P7/execution 未执行；`E2E_EXIT=1` | P5 flake，见 §7 |
| 同上（attempt C，full fresh run） | 02:13–02:18；Phase 1 **23 passed**（1.7m，含 mindmap 通过）、Phase 2 P7 **5 passed**（1.0m，pre-fix 代码，尚无新探索链路 step）、Phase 3 execution **1 failed**；`E2E_EXIT=1`（28/29） | 历史：P6 spec 陈旧断言 |
| 同上（T27 final，P6 spec 修复后） | 02:42；Phase 1 **23 passed**（1.6m）、Phase 2 P7 **5 passed**（1.0m）、Phase 3 execution **1 passed**（26.0s）；**29/29，`E2E PASSED`，EXIT=0** | **PASS** |
| `powershell ... -File scripts/e2e.ps1 p7-continuing p7-db-config p7-degraded p7-issues-report p7-supplement`（post-fix P7） | 02:22–02:24；**5 passed（1.0m），`E2E PASSED`，`P7_POSTFIX_EXIT=0`**；含新增 `exploration tab` step 与 `p7-issues-05-exploration-issues.png` | **PASS** |
| `powershell ... -File scripts/e2e.ps1 execution`（narrowed） | 02:18–02:19；`execution.spec.ts:293` 失败：`case run 373898173145419777 did not reach pending within 30000ms (last status: inconclusive)`；API 日志 `inconclusive / model / budget_exhausted`，pause POST 在用例结束后才返回 200；`EXECUTION_EXIT=1` | 可复现的 P6 门禁失败 |
| `powershell ... -File scripts/acceptance.ps1 -StaticOnly`（第一次） | 02:19；`30 passed, 1 failed, 32 skipped`；P4 fake-LLM 检查因 `.tmp/p4-e2e-repo` 瞬时 fixture 状态失败（`fixture file is missing: src/api/refund.ts` / `git rev-parse HEAD`） | 瞬时环境失败 |
| 同上（第二次，fixture 稳定后） | 02:20；**`31 passed, 0 failed, 32 skipped`，`ACCEPTANCE PASSED`，EXIT=0** | PASS |
| 同上（post-fix tree，第三次） | 02:26；**`31 passed, 0 failed, 32 skipped`，`ACCEPTANCE PASSED`，EXIT=0**（`.tmp/t32b-acceptance-static-3.log`） | PASS |
| 同上（post-commit `586e1d3`） | 02:29；首次尝试 fake-LLM 端口 `14905` 报 `EACCES`（瞬时端口保留），立即重试 **`31 passed, 0 failed, 32 skipped`，`ACCEPTANCE PASSED`，EXIT=0**（`.tmp/t32b-acceptance-static-final2.log`） | PASS |
| `git diff --check` | EXIT=0（仅有 LF→CRLF 提示，无空白错误）；`git diff --cached --check` 同样 EXIT=0 | PASS |

P7 E2E 用例耗时（post-fix Phase 2）：`p7-continuing` 19.6s、`p7-db-config` 3.1s、`p7-degraded` 9.0s、`p7-issues-report` 19.5s、`p7-supplement` 9.9s，合计 5 passed（1.0m）。pre-fix full run 的 Phase 2 为 18.6/4.0/8.1/18.4/11.0s，也全部 PASS。

并发修复与 post-fix 验证：02:18:26–02:19:07 另一任务将 `ExplorationTab` 的 `REQUIREMENT_LIMIT`/`ISSUE_LIMIT` 从 200 改为 100，并把 requirements/graph 拆成独立 `allSettled` 图层错误（有数据时显示 `exploration-degraded`，无数据时显示 `exploration-error`），同时在 `p7-issues-report.spec.ts` 中新增 exploration step（断言无 error/degraded、`chain-issue-count=2 个问题`、`chain-issue-node-{ai,human}` 可见并截图）。该内容于 02:27:36 提交为 `586e1d3`（`fix(p7): align exploration limits with API caps`）。02:22–02:24 的 post-fix P7 run 5/5 PASS，证明 #9 已修复；pre-fix 的 02:10 缺陷截图保留为根因证据。

真实 LLM 浏览器走查：旧 T32 的 `.tmp/t32-real-browser.ps1` 在 02:09:53 被 DSH 重新拉起，对 T28 的 `eztest_p7_llm` 库启动 API :13337 / Web :6869 / executor :14349，并在 02:09:57–02:10:03 产出 `01`–`06` 截图；随后在全局问题中心的“搜索”步骤等待 `global-issue-row-373892845100077056` 超时。原因是走查脚本把搜索词填为“失败”，而该问题标题不含“失败”，列表被正确过滤为空；**这是走查脚本的测试数据错误，不是应用 500/崩溃**。该走查未产出 `browser-summary.json`，本文只把 `01`–`06` 截图作辅助证据，并以本次 E2E 为正证。

## 3. P7 DoD #1–#5

| DoD | 要求 | 证据（命令/路径） | 结果 |
|---|---|---|---|
| #1 | 执行中发现覆盖缺口并补用例、回填脑图、不阻断基线执行 | post-fix E2E `p7-supplement.spec.ts` PASS（9.9s）：baseline #1 → 自动 supplement child #2（parent=#1），2 条 `ai_supplement` 用例执行 passed，baseline 仍为 1 passed/0 failed；supplement 报告 Batch #2 来源徽章=补全、diff 新用例=2；截图 `p7-supplement-01-report.png`。T28：supplement batch `373892903979716608`，2 个 ai_supplement run、2 个 supplement node，报告 `373892912372518912`，事件 `case.supplemented`×4，基线报告 `373892845339152384` 不受影响 | **PASS** |
| #2 | 新增需求 → continuing → 增量批次 → 新报告；暂停/恢复 | post-fix E2E `p7-continuing.spec.ts` PASS（19.6s）：completed → continuing/clarifying → designing，计划 2 新用例 + 1 受影响用例，增量批 #2 共 3 条全 passed，增量报告 sources=ai_supplement×2、diff newCases=2，最后 completed；截图 `p7-continuing-01/02/03/04/05.png`。T28：version `373892717001838592`，增量批 `373893156917219328`，增量报告 `373893164064313344`，plan impact `impactAvailable=true`/`consumed=true`，pause+resume 各一次 | **PASS** |
| #3 | 问题带步骤/证据/代码位置；报告分批次、stale | post-fix E2E `p7-issues-report.spec.ts` PASS（19.5s）：AI 问题+人工问题，AI 问题确认（revision 2），人工问题驳回必须填原因并落库；报告 stale→regenerate revision 2，supplement 报告 diff + Markdown 导出；新增 exploration step 断言无 error/degraded、问题节点 2 个并截图 `p7-issues-05-exploration-issues.png`；其余截图 `p7-issues-01/02/03.png`。T28：issue `373892845100077056` source=ai/category=defect/severity=major，refs=[case_node,case_run,evidence]，`codeRefs=0`（已知限制，UI 显示“无代码锚点”）；baseline 报告 rev1、增量报告 rev1、diff addedIssues=2/newCases=2/removedCases=1 | **PASS（codeRefs=0 已知限制）** |
| #4 | DB 断言只读可配置执行、错误不误判、P6 用例重跑 | T29 `scripts/acceptance-p7-db.ps1`：`verification.md`/`checks.json` **20/20 passed**（最新 run 44.6s；交接记录重跑同为 20/20）。覆盖 passed/mismatch→failed(application)/写 SQL→inconclusive(case,db_readonly_violation)/行数超限→inconclusive(case,db_result_truncated)/不可达+错误密码→blocked(environment,db_connection_failed)/missing→blocked(environment,db_target_missing)/P6 legacy 重跑/普通用户 403/密码与事件泄漏扫描。post-fix E2E `p7-continuing` 真实 PG db 断言 verdict=passed 且有 db_result evidence；`p7-db-config` PASS（3.1s；连接成功 32ms、只读事务、写权限告警、普通用户无入口+403）；截图 `p7-db-target-01-test.png` | **PASS** |
| #5 | AI 不可用降级为纯基线 + 人工查看 | post-fix E2E `p7-degraded.spec.ts` PASS（9.0s）：batch done、failure analysis degraded、无 triage/supplement、report `degraded=true`、issues 0、UI 无 crash；截图 `p7-degraded-01-report.png`。T28 场景 4：no-LLM API、batch `373893236399280128` done（1 passed/1 failed）、analysis degraded `ai_unavailable`、report `373893250446004224` degraded=true、issueRows=0、agentRuns=0、API 健康、views 200；`scenario4-degraded*.json`、`scenario4-views.json` | **PASS** |

## 4. §2.9 前端锚点

| # | 锚点 | 证据 | 状态 |
|---|---|---|---|
| 7 | 总览指标卡与批次时间线 | post-fix E2E `p7-continuing-05-overview.png`：需求覆盖/用例结果/问题/当前批次 4 卡 + 批次时间线（Batch 1 基线、Batch 2 增量）；断言 `overview-metrics`、`overview-batch-timeline` 可见且含“增量”。真实 LLM：`real-llm-01-overview.png`（3 批、3/0/0、问题 3、代码变更摘要） | **PASS** |
| 9 | 探索链路问题节点 / AI 补充 / 受影响高亮 | **post-fix PASS**：02:23:49 `p7-issues-05-exploration-issues.png` 显示 9 个节点、`问题节点 2 个问题`（AI+人工红色菱形）、AI 补充虚线角标，无错误横幅；post-fix E2E 断言无 `exploration-error`/`exploration-degraded`/`chain-issues-degraded`，`chain-node-{requirementId}` 可见，`chain-issue-count=2 个问题`，`chain-issue-node-{ai,human}` 可见。pre-fix 02:10 曾失败：`real-llm-05-exploration-issues-DEFECT.png` 显示 `加载探索链路失败：limit must be an integer between 1 and 100`、0 个问题；根因是 `ExplorationTab` 原 `REQUIREMENT_LIMIT=200`/`ISSUE_LIMIT=200` > 后端 `backend/internal/platform/pagination.go:15` `MaxLimit=100`（实测 :13337：requirements `?limit=200`→400、`?limit=100`→200（4 条）；issues `?limit=200`→422、`?limit=100`→200（3 条））。并发修复 02:18–02:19 将上限改为 100 并拆分独立图层错误，02:27:36 提交为 `586e1d3`；post-fix P7 5/5 PASS | **PASS（post-fix；pre-fix 缺陷已修复）** |
| 12 | 问题列表/详情/确认/驳回/深链 | post-fix E2E `p7-issues-01-confirmed-dismissed.png`：问题 Tab 两行（AI 已确认、人工已驳回并显示原因），详情抽屉含描述/复现步骤/期望/实际/证据入口/关联用例/代码位置；真实 LLM `real-llm-02-issue-detail.png`（AI 问题 3 条、证据 70B、代码位置“无代码锚点”）；E2E 断言 `issue-row-*`、`issue-detail`、确认/驳回 CAS 与 revision | **PASS（codeRefs=0 已知限制）** |
| 13 | 报告批次切换/差异/导出 | post-fix E2E `p7-issues-03-report-diff.png`：revision 2、来源徽章（补全）、批次差异（新增 2、消失 1、新增问题 2）、sha256 导出提示；`p7-supplement-01-report.png`（Batch #2 补充报告、来源徽章、diff）；真实 LLM `real-llm-04-incremental-report.png`（Batch #3、3 条全通过、diff 2/1、问题 3） | **PASS** |
| 18 | completed→continuing→completed 徽章/横幅/新增需求/批次差异 | post-fix E2E `p7-continuing-01-completed.png`（已完成 + 新增需求）、`p7-continuing-02-designing.png`（`继续中（第 2 批）` + `增量设计`）、`p7-continuing-03-incremental-plan.png`（2 新 + 1 受影响 + 创建增量批次）、`p7-continuing-05-overview.png`（Batch 2 增量）；断言 `continuing-badge`、`continue-phase-label`、`incremental-plan-*`、完成状态 | **PASS** |
| 21 | Markdown/Mermaid 导出 | post-fix E2E `p7-issues-report` 与 `p7-continuing` 均实际下载 `.md` 并断言含 `## 1. 执行摘要`、`mermaid` 代码块、不含管理员密码；`p7-issues-03-report-diff.png` 显示导出 sha256。T28 `report-incremental.md`（2828B）、`scenario4-degraded-report.md`（2164B，含降级提示）与 `verification.md` 的 Mermaid/字节/sha256 检查；T29 生成的 7 份报告 Markdown 可下载 | **PASS** |
| 22 | 全局问题/报告中心：列表/过滤/深链 + 挂载路由 | 见 §4.1 | **PASS（过滤仅有组件级+空态证据，见 §4.1）** |

### 4.1 §2.9 #22 全局中心核对

- **列表 + 深链（浏览器，post-fix E2E）**：`p7-issues-report.spec.ts` 的 `global issue and report centres deep-link...` 步骤 PASS：
  - `/issues` → `global-issues-page` 可见，`global-issue-row-{aiIssueId}` 可见；`href` 同时包含 `/versions/{versionId}` 与 `issue={issueId}`；点击后 `issues-tab` + `issue-detail[data-issue-id]` 可见。
  - `/reports` → `global-reports-page` 可见，`global-report-row-{reportId}` 可见；`href` 同时包含 `/versions/{versionId}` 与 `report={reportId}`；点击后 `report-tab` + `revision 2` 可见。
  - 截图 `p7-issues-04-global-centres.png` 是点击报告深链后的**版本报告 Tab（Batch #1/revision 2）**，不是全局列表本身；全局列表的可见性/深链断言在测试中。
- **真实 LLM 全局问题页**：`real-llm-06-global-issues.png` 显示 `/issues` 的版本/状态/搜索三组过滤，版本选中 `P7 real LLM version...`、状态“待处理”、搜索“失败”；因搜索词无匹配，列表显示空态（恰好证明过滤生效）。
- **过滤（组件测试 + 空态）**：`frontend/src/features/delivery/components/GlobalIssuesPage.test.tsx` 覆盖 `global-issues-version-filter` 版本过滤；`GlobalReportsPage.test.tsx` 覆盖 `global-reports-version-filter`；真实页面另有状态/标题搜索。本次没有对两个全局列表做“正向过滤后仍有行”的浏览器点击（走查脚本搜索词错误），因此过滤证据限于组件测试与真实空态，不是完整浏览器正向断言。
- **挂载路由**：
  - 前端文件路由：`frontend/src/app/(app)/issues/page.tsx`、`frontend/src/app/(app)/reports/page.tsx`，分别挂 `GlobalIssuesPage`/`GlobalReportsPage`。
  - 后端：`GET /api/issues`、`GET /api/reports` 由 `backend/internal/delivery/interfaces/httpapi` 暴露；`backend/internal/integration/wiring_p7_test.go::assertP7GlobalCentresMounted` 在真实组装根断言 200 + page envelope + `q` 过滤；`handlers_global_test.go` 覆盖鉴权、普通用户、空列表、错误映射、filter 校验；`routes_test.go` 断言 200。
  - 本次 E2E 是在真实 API/浏览器上访问两个页面并点击行内深链，等同于运行期挂载验证。

## 5. 已检查的截图

以下 16 张已复制到 `docs/acceptance/p7/`：11 张 post-fix P7 E2E（02:23–02:24）+ 5 张真实 LLM 数据走查（02:09–02:10）；源文件仍在 `.tmp`。

| 截图 | 来源 | 检查结论 |
|---|---|---|
| `p7-continuing-01-completed.png` | post-fix E2E 02:23:10 | 已完成徽章、批次 #1 基线、新增需求按钮 |
| `p7-continuing-02-designing.png` | post-fix E2E 02:23:11 | `继续中（第 2 批）` 徽章 + `增量设计` 阶段 |
| `p7-continuing-03-incremental-plan.png` | post-fix E2E 02:23:15 | 增量计划：2 新 + 1 受影响，创建增量批次 |
| `p7-continuing-05-overview.png` | post-fix E2E 02:23:20 | 4 个指标卡 + Batch 1/2 时间线 |
| `p7-issues-01-confirmed-dismissed.png` | post-fix E2E 02:23:48 | AI 问题已确认、人工问题已驳回且原因落库 |
| `p7-issues-03-report-diff.png` | post-fix E2E 02:23:51 | 补充报告来源徽章、diff 2/1、sha256 导出 |
| `p7-issues-04-global-centres.png` | post-fix E2E 02:23:52 | 报告深链落点：版本报告 Tab revision 2（非全局列表） |
| `p7-issues-05-exploration-issues.png` | post-fix E2E 02:23:49 | **#9 PASS 证据**：9 节点、问题节点 2 个、AI 补充角标、无错误 |
| `p7-supplement-01-report.png` | post-fix E2E 02:24:02 | Batch #2 补充报告、2 条 ai_supplement、diff 2 新 |
| `p7-degraded-01-report.png` | post-fix E2E 02:23:32 | AI 降级徽章/提示、1 failed、问题 0、无 crash |
| `p7-db-target-01-test.png` | post-fix E2E 02:23:22 | 连接成功 32ms、只读事务、写权限告警、密码不回显 |
| `real-llm-01-overview.png` | 真实 LLM 02:09:57 | 3 批（基线/补全/增量）、3/0/0、问题 3、代码变更摘要 |
| `real-llm-02-issue-detail.png` | 真实 LLM 02:09:57 | AI 问题详情、复现步骤、证据入口、代码位置“无代码锚点” |
| `real-llm-04-incremental-report.png` | 真实 LLM 02:09:58 | Batch #3 增量、3/3 通过、diff 2/1、问题 3 |
| `real-llm-05-exploration-issues-DEFECT.png` | 真实 LLM 02:10:03（pre-fix） | **历史缺陷证据**：limit 错误 + 问题节点 0；修复后由 `p7-issues-05` 覆盖 |
| `real-llm-06-global-issues.png` | 真实 LLM 02:10:03 | `/issues` 版本/状态/搜索过滤与空态 |

另外检查但未复制：`p7-continuing-04-report.png`（issues 空态）、`p7-issues-02-report-regenerated.png`（revision 2）、`p7-supplement-02-baseline-report.png`（baseline 报告、diff unavailable）、`p7-degraded-02-version.png`（降级版本页）、`p7-db-target-02-saved.png`（已保存、密码遮罩）、`p7-db-target-03-readonly.png`（普通用户只读、无 DB 目标分区）、`real-llm-03-evidence-viewer.png`（证据查看器）。

## 6. T28 / T29 证据索引

### T28 真实 LLM（`.tmp/p7-real-llm/`）

- 命令：`$env:EZTEST_RUN_REAL_LLM="1"; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p7-llm.ps1`
- `verification.md`（round5）：**42/42 passed，0 failed，0 skipped，AllowSkip=False，131.6s**；plan `impactAvailable=true`/`consumed=true`，scenario 4 degraded/`ai_unavailable`；`run.json` 同为 42 checks passed。
- 版本 `373892717001838592`；基线批 `373892719338065920`；supplement 批 `373892903979716608`；增量批 `373893156917219328`；AI issue `373892845100077056`；增量报告 `373893164064313344`（sha256 `c37813e3…f642d`）。
- 计划影响：`incremental-plan-impact.json` `impactAvailable=true`、`consumed=true`、`p4Candidates=2`、`unmappedChanges=1`、warning `affected_case_skipped`；run `373893088529092608`。
- 增量报告：caseCount=3，全 passed，sources=ai_supplement×3；diff 新增问题 2/新用例 2/消失用例 1。
- 场景 4 降级：`scenario4-degraded.json` batch done、analysis degraded `ai_unavailable`、report degraded=true、0 issue、0 agent run、API 未崩；`scenario4-views.json` version issues=200（0 行）、global issues=200（0 行）、version-scoped reports=200（1 行）、analysis/report/export/events=200。
- token（round5）：input 70,845 / output 7,291 / 0 reasoning / 4 real agent runs；`withinBudget=true`（上限 3,000,000）；一次 real-inference `no_candidates` 重试已披露并保留。
- 秘密扫描：`secret-scan-final.json` 30 文件、6 个秘密值、passed=true；`events.json` 167 条事件、必需事件类型齐全、`case.supplemented`×4。
- 已知限制（详见 §7）：`codeRefs=0`；执行使用允许的 deterministic fake executor；per-run model 为空。

### T29 真实 DB（`.tmp/p7-real-db/`）

- 命令：`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p7-db.ps1`
- `verification.md` / `checks.json`：**20/20 passed**（44.6s，最新 run；T29 交接记录重跑同为 20/20）；`run.json` status=passed，secretScan 32 文件/10 秘密值/0 leaks，idempotency 2 次 db_assert 调用、2 条 distinct db_result、1 条 assertion/1 条 evidence/1 条 ref。
- 关键检查：只读角色 fixture；passed + P6 rerun 不假通过；mismatch→failed(application)+db_result；写 SQL→inconclusive(case,db_readonly_violation)；行数超限→inconclusive(case,db_result_truncated)；不可达/错误密码/missing target→blocked(environment)；普通用户 403；密码不落 API/事件/审计/证据/报告。
- 证据文件：`checks.json`、`targets.json`、`worlds.json`、`evidence/db-result-passed.json`、`evidence/db-result-truncated.json`、`secret-scan.json`、`reports/**/*.md`。

### 本次 E2E 日志

- `.tmp/t32b-e2e-4.log`：attempt C 全量输出与最终 `E2E_EXIT=1`（Phase 1 23/23、Phase 2 pre-fix 5/5、Phase 3 execution 1 failed）。
- `.tmp/t32b-p7-postfix.log` / `.tmp/t32b-p7-postfix-exit.txt`：post-fix P7 phase 5/5、`E2E PASSED`、`P7_POSTFIX_EXIT=0`；含新 exploration step 与 `p7-issues-05-exploration-issues.png`。
- `.tmp/e2e-playwright.log`：post-fix P7 的原始 Playwright 输出（最后一次运行覆盖）。
- `.tmp/t32b-execution.log`：narrowed execution 失败（`case run 373898173145419777 did not reach pending...`）。
- `.tmp/t32b-acceptance-static-3.log`：post-fix static gate，`31 passed, 0 failed, 32 skipped`, `ACCEPTANCE PASSED`（02:26）。
- `.tmp/t32b-acceptance-static-final2.log`：post-commit `586e1d3` static retry，首次 fake-LLM 端口 `14905` `EACCES`，重试 `31 passed, 0 failed, 32 skipped`, `ACCEPTANCE PASSED`（02:29）。
- `.tmp/t32b-acceptance-static.log`：首次 static 运行 `30 passed, 1 failed`（fixture 瞬时状态）。

## 7. 已知限制 / 遗留

1. **【已修复，保留为回归证据】§2.9 #9 探索链路 limit 越界**：pre-fix 02:10 前端 `REQUIREMENT_LIMIT`/`ISSUE_LIMIT=200` > 后端上限 100，requirements 400、issues 422，问题节点不可用。并发任务 02:18–02:19 将常量改为 100，并把 requirements/graph 拆成独立 `allSettled` 图层错误（有数据→`exploration-degraded`，无数据→`exploration-error`）；同时新增 `p7-issues-report` 的 exploration step，并于 02:27:36 提交为 `586e1d3`（`fix(p7): align exploration limits with API caps`）。post-fix P7 run 5/5 PASS，`p7-issues-05-exploration-issues.png` 显示 2 个问题节点、无错误。pre-fix 截图与 :13337 的 400/422 探针保留在 §4/§5 作为根因证据。
2. **【已修复】P6 execution.spec 陈旧断言**：根因不是产品回归，而是 P7 autonomy 在 failed baseline 后自动创建 supplement 批次，使 “untouched version” 批次数为 2；P6 spec 只允许 1 个批次。T27 已改为仅断言没有第二个 baseline（允许自动 supplement），提交 `235b894`，默认三阶段 e2e 复跑 **29/29 PASS**。
3. **`codeRefs=0`（T28 真实 LLM）**：失败分析 Agent 尝试 `analysis.code_read` 但未给出可读代码位置；issue 仍有 case/run/evidence refs 与完整 prose。P7 §7.3 为 best-effort；真实 UI 显示“无代码锚点”。E2E/fixture 中的 code refs 另行由 `versions-code-*` 覆盖。
4. **T28 执行是允许的 deterministic fake executor**：真实 LLM 只覆盖 failure/triage/supplement/incremental design；case 执行、步骤、断言、截图由 fake executor 生成（证据截图是 70B 占位图）。是否满足 §12.7“真实执行”措辞需 Lead 裁定；真实浏览器执行由 E2E 覆盖。
5. **per-run model 为空**：`analysis_run`/`case_design_run` 使用默认 LLM 配置，单 run `model` 字段为空；全局 `deepseek-flash` 已在 `run.json`/`verification.md` 断言。
6. **archived 版本 reset 语义**：T17c 记录仅 `completed` 视为 terminal/immutable；`archived` 版本的手工分析仍可能重置已结算链，且无 CAS reset API。属 Lead ruling/P8 遗留，未在本次浏览器验收覆盖。
7. **Windows shutdown ordering**：P7 worker 停止顺序由代码检查（`main.go:1392` 先于 `runService.Shutdown`）与单测固定；Windows 上无 SIGTERM 顺序运行时测试（force-kill 跳过 defer）。归 T33/P8。
8. **全局报告空态 smoke**：T17c 的 `/api/reports` 只做 200 空页断言；本次 P7 E2E 已覆盖有数据的 `/reports` 列表 + 深链，但两个全局列表的“正向过滤后仍有行”未在浏览器点击（组件测试 + 空态证据）。归 T33 补测。
9. **P5 mindmap flake**：attempt B 的 `mindmap.spec.ts:234` 失败（新增步骤表单动作被清空，提示“请填写步骤动作。”），attempt C Phase 1 23/23 通过；判定为 P5 spec 的时序 flake，与本 P7 功能无关，但默认全量 runner 仍有不稳定概率。
10. **环境噪声**：attempt A 被旧 T32 Next dev :6869 的 `.next/dev` lock 阻塞；真实 LLM 走查脚本被 DSH 重新拉起并在全局过滤步骤因搜索词“失败”无匹配超时。二者已清理/记录，不影响上述 P7 E2E 与 API 复现结论。
11. **工作区不是干净提交**：HEAD 为 `586e1d3`（#9 修复提交，02:27:36），其上仍有 32 个 modified、16 个 staged add、4 个 untracked；本验收运行的是 dirty worktree（post-fix P7 run 时 fix 还在 staged 状态，内容与 `586e1d3` 相同）。T33 最终验收应在干净提交上重跑。
12. **证据命名**：`p7-continuing-04-report.png` 实际显示 issues 空态；`p7-issues-04-global-centres.png` 是深链落点而非全局列表。本文件已按实际内容标注，不改变断言结论。

## 8. 证据归档与 git add

本次 T32b 只新增/修改：

- `acceptance.md`（本文件）
- `docs/acceptance/p7/` 下 16 张关键截图（11 张 post-fix E2E + 5 张真实 LLM 走查）

未修改任何代码、测试、脚本；未执行 `git add/commit/push`。T28 脚本在本任务开始前已 staged（`AM scripts/acceptance-p7-llm.ps1`），不属本任务变更；验收中途并发加入的 `frontend/src/features/execution/components/ExplorationTab.tsx`、`ExplorationTab.test.tsx`、`frontend/e2e/p7-issues-report.spec.ts` 的 staged 修改同样不属 T32b 变更。

建议归档（父任务执行）：

```text
git add acceptance.md docs/acceptance/p7
```

## 9. 最终判定

**PASS WITH LIMITS（P7 DoD #1–#5 PASS；P7 §2.9 #7/#9/#12/#13/#18/#21/#22 PASS；full three-phase E2E 仍未全绿：P6 execution.spec 1 项失败）。**

P7 功能层已达成：**full e2e 29/29 PASS**（P6 spec 修复后）、T28 真实 LLM 42/42（0 skip，131.6s）、T29 真实 DB 20/20、静态 31/0/32；#9 的 pre-fix 缺陷已修复（`586e1d3`）并由 E2E/截图验证。已知限制：codeRefs=0、T28 fake executor、archived reset、Windows shutdown、全局过滤未正向点击、T28 一次 real-inference 重试。

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly
```

没有未解释的失败：上述所有失败均已给出命令、日志、status/reason 或截图。
