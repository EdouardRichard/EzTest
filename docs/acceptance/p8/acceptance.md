# EzTest P8 最终真实验收记录（Task 35）

- 验收日期：2026-10-29（19:02–22:46，Asia/Shanghai）
- 分支 / 最终提交基线：`dev`；验收开始时 HEAD `c23ce67`，结束时 HEAD 含并行修复 `1b457d2`、`53a168a`、`a736a5a`、`625cf57`、`751f016`、`2cbf2f6`（P8 SLO 真实测量、格式/深链/E2E 预期/审计分页/审计 request-id 测试修复）。
- 环境：Windows 11 + Windows PowerShell 5.1.26100；本机 PostgreSQL 17（`.tools/pgsql`，`localhost:5433`）；Node `v24.18.1`；pnpm `11.28.5`；Go `go1.24.6`；真实 Google Chrome（Playwright `channel=chrome`）；真实 `llm_*`（DeepSeek `deepseek-flash`，密钥不打印）。
- 固定端口：API `:13333`、Web `:6868`、fixture `:13335`、executor `:13336`。
- 证据根目录：`docs/acceptance/p8/`（提交）、`.tmp/p8-final-20261029-192239/`（本任务日志）、`.tmp/p8-final-llm-20261029-210545/`（真实 LLM 运行日志）、`.tmp/p8-backup/`（备份演练）、`.tmp/p8-eval/`（评测）。
- 结论：**PASS WITH LIMITS**。P8 功能、真实 LLM + 真实 Chrome 全流程、全量 E2E、确定性评测、真实备份恢复 + 恢复库应用 smoke、实时安全审计均通过；**复合 `scripts/acceptance.ps1` 全量门禁未能在本机跑绿**（宿主负载导致的 Go 集成包 600s 超时与 3 个 lazy-load 组件测试超时，另有一次 PostgreSQL 因磁盘写满崩溃后恢复），因此不宣称“全量 acceptance 绿”。逐项限制见 §9。

## 1. 判定与不夸大声明

| 项目 | 结果 |
|---|---|
| 全量后端/前端/executor 测试（`scripts/test.ps1`） | **PASS** |
| 静态验收（`scripts/acceptance.ps1 -StaticOnly`） | **PASS**（最终修复后 **34 passed / 0 failed / 34 skipped**；审计策略新增 1 项检查） |
| 复合全量验收（`scripts/acceptance.ps1`） | **未跑绿**（本机宿主负载超时；见 §2.2） |
| 全量 Playwright E2E（`scripts/e2e.ps1`，workers=1） | **PASS**（26 + 9 + 5 + 1 = 41 passed） |
| 确定性评测（`scripts/eval.ps1`） | **PASS**（6 门禁指标 + 视觉指标全部 PASS） |
| 真实 LLM + 真实 Chrome（`scripts/acceptance-p8-llm.ps1`） | **PASS**（29/29 checks，17 张截图，0 console error，0 秘密泄露） |
| SLO 真实测量（`scripts/slo.ps1` 最新报告） | **PASSED 6/6**（Task 27 在 `1b457d2` 的真实测量；本任务读取并核对报告，未改写 `scripts/slo.ps1`） |
| 备份/恢复演练（`scripts/test-backup.ps1 -Keep`） | **PASS**（174/174 assertions） |
| R33 恢复库应用 smoke | **PASS**（`/healthz`、`/api/health`、`/api/openapi.yaml`、管理员登录、`/api/versions` 5/5） |
| 安全审计（`scripts/security-audit.ps1`，live） | **PASS**（45 PASS / 1 SKIP / 0 FAIL；SKIP 为未安装 `govulncheck`） |

> “PASS WITH LIMITS”的依据：所有**产品功能与真实环境**门禁均通过；唯一未跑绿的是复合 acceptance 脚本在**共享、外部高负载主机**上的计时类失败，以及 §9 列出的固有边界（Docker/CI、GC 范围、SLO 本地口径等）。不把任何 SKIP/环境失败计为 PASS。

## 2. 门禁执行记录

### 2.1 通过项

| 命令 | 时间 / 结果 | 日志 |
|---|---|---|
| `powershell.exe -File scripts/test.ps1` | 19:22–19:34；**All selected checks passed**。Go unit + integration；executor 89；P7 fake 17；P6 fake 16；frontend 138 files / **1183 tests**；lint/typecheck 通过 | `.tmp/p8-final-20261029-192239/test.log` |
| `powershell.exe -File scripts/acceptance.ps1 -StaticOnly` | 19:32–19:34；**Result: 33 passed, 0 failed, 34 skipped**；`ACCEPTANCE PASSED` | `.tmp/p8-final-20261029-192239/acceptance-static.log` |
| `powershell.exe -File scripts/acceptance.ps1 -StaticOnly`（最终修复后复跑） | 2026-10-29 22:5x；**Result: 34 passed, 0 failed, 34 skipped**；`ACCEPTANCE PASSED`；新增 `P8 audit request_id policy` 自检（`2cbf2f6`） | 本轮 Lead 复跑输出 |
| `powershell.exe -File scripts/e2e.ps1` | 22:36–22:42；**E2E PASSED**；existing suite **26 passed** (1.8m)、P8 cross-wave **9 passed** (1.1m)、P7 **5 passed** (1.0m)、execution **1 passed** (32.0s)；Playwright `workers: 1`（`frontend/playwright.config.ts`） | `.tmp/p8-final-20261029-192239/e2e-full-2.log` |
| `powershell.exe -File scripts/eval.ps1` | 20:44–20:47；**EVAL PASSED**；quality run `374176831311454208`；6 门禁指标 + `visual_classification_accuracy` 全部 PASS | `.tmp/p8-final-20261029-192239/eval-deterministic.log`；`.tmp/p8-eval/result.json` |
| `EZTEST_RUN_REAL_LLM=1 powershell.exe -File scripts/acceptance-p8-llm.ps1` | 21:05–21:12；**29 checks / 0 failures / 17 screenshots**；真实 DeepSeek + 真实 Chrome；secret scan 13 files / 0 leak；console error 0 | `real-llm/result.json`；日志 `.tmp/p8-final-20261029-192239/acceptance-p8-llm-6.log` |
| `powershell.exe -File scripts/test-backup.ps1 -Keep` | 21:47–21:49；**174 assertions passed**；68 表行数一致；默认布局 58/58 文件校验；backup 8.44s / restore 1.51s | `.tmp/p8-final-20261029-192239/backup-drill.log`；`.tmp/p8-backup/test-report.json` |
| R33 smoke（`.tmp/p8-llm-dev/backup-smoke.ps1`） | 21:50–21:52；5/5 PASS：`/healthz` 200、`/api/health` ok/ok、`/api/openapi.yaml` 200（680,007 B）、恢复库管理员登录 200、`/api/versions?limit=5` 200（5 行） | `.tmp/p8-final-20261029-192239/backup-app-smoke.json` |
| `powershell.exe -File scripts/security-audit.ps1 -SkipLive` | 19:35–19:37；**32 PASS / 2 SKIP / 0 FAIL** | `.tmp/p8-final-20261029-192239/security-audit-static.log` |
| `powershell.exe -File scripts/security-audit.ps1`（API live） | 22:42–22:43；**45 PASS / 1 SKIP / 0 FAIL**；实时权限 401/403/200 矩阵、临时用户创建/禁用清理、logout 均在真实 API 上执行 | `.tmp/p8-final-20261029-192239/security-audit-live.log` |
| SLO 报告核对 | 读取 `docs/acceptance/p8/slo.md` / `slo-metrics.json`（生成 `2026-10-29T11:18:28Z`，Task 27 提交 `1b457d2`）；**6/6 PASSED** | `slo.md`、`slo-metrics.json` |

### 2.2 复合 `scripts/acceptance.ps1` 未跑绿的环境失败（已解释）

该脚本要求把 Go/Agent/Version 集成、前端全量、生产构建、E2E、P5/P6 API、评测串行跑完；在**本机被其它应用占用 CPU/IO** 的条件下出现计时类失败，而每个组成套件均已单独跑绿：

| 尝试 | 结果 | 根因 / 处置 |
|---|---|---|
| #1 | 41 passed / 7 failed / 11 skipped | 磁盘写满（本机 D: 仅剩 2.6 MB）导致 PostgreSQL 进入 recovery/57P03，API/E2E/集成连锁失败；清理构建产物并等待 PG 崩溃恢复完成后重跑 |
| #2 | Go integration 通过；后续因 PG recovery 连锁失败 | 与 #1 同一磁盘事件的后半段 |
| #3 | 前端 138 files / **1180 passed + 3 failed** | 外部高负载下 3 个 lazy-load 画布测试超时（`ChainCanvasSpike`、`ChainCanvas`、`ExplorationTab`）；这 3 个文件**单独运行全部通过** |
| #4 | **67 passed / 1 failed / 11 skipped** | 唯一失败：审计检查只取最新 100 条，P8 E2E 后 `user.created` 被挤出首页。已在真实 `eztest_e2e` 库验证：`audit_log` 1445 行、`user.created` 11 行、最新 100 行中 0 行；这是**测试分页问题，不是产品缺陷**。已由并行修复 `751f016 test(p8): query audit actions by filter` 改为按 `action=` 过滤查询 |
| #5/#6 | Go integration 包 600s 超时 | 宿主负载下 `version/infrastructure/postgres` 等包在 600s 边界超时；同一包在 `test.ps1` 与 #4/#5 的其它运行中通过（如 `test.ps1` 的 `resource/infrastructure/postgres` 450s、`version/integration` 535s；#6 中 `version/integration` 596s 通过）。属环境性能不稳定，非功能断言失败 |

补充：对 #4 的审计失败，数据库查询已证明产品审计正常（11 条 `user.created`），修复提交 `751f016` 把验收脚本改为按 action 过滤；后续 `2cbf2f6` 又对 worker/finalizer/GC 背景动作做 request_id 窄豁免（HTTP 动作仍强制），并在真实 `eztest_e2e` 上通过 live `Test-AdminAudit`。静态验收最终复跑 34/0/34 PASS；但复合 `acceptance.ps1` 全量门禁未再获得一次完整绿色运行（后续两次停在宿主负载的 Go 包 600s 超时），因此本记录**不声称复合门禁 PASS**。

## 3. 每波 DoD 证据索引（W0–W5）

| 波次 | 证据 | 覆盖内容 |
|---|---|---|
| W0 | `wave0/defect-verification.md`、`wave0/exploration-fixed.png`、`wave0/exploration-issue-node.png` | 重建生产产物；缺陷 #1（探索链路 limit 200→100）在修复后不再复现；真实 Chrome 断言 7 链路节点 + 2 问题节点、0 console error |
| W1 | `wave1/exploration-chain.md`、`wave1/react-flow-spike.md`、`wave1/p8-chain-01..06.png` | 探索链路四类节点/问题 Sheet/筛选/折叠/截断/重连；200 节点 axe/键盘；链路 API P95 92.2 ms、画布交互 P95 1109 ms |
| W2 | `e2e/README.md`、`e2e/p8-shell-01/02/03/04/05.png`、`e2e/p8-version-detail-01..05.png` | 版本切换/待办/全局搜索/移动导航；脑图 Tab 与全屏路由；代码与变更 batchTimeline；报告 revision/stale |
| W3 | `e2e/README.md`、`e2e/p8-management-01/02/03/05.png`、`security-audit.md` | 普通用户只读/管理员写；13 角色目录；审计筛选/详情/空态；基础配置只读与 GC dry-run；375/768 导航（W2/W3 共享）；实时权限矩阵 45 PASS |
| W4 | `e2e/README.md`、`e2e/p8-quality-01..08.png`、`../../eval/README.md`、`../../eval/baseline.json` | 知识草稿→审批→可见；视觉基线审批→真实 executor→`visual_actual`/`visual_diff`；质量中心 6 指标；确定性评测语料/基线 |
| W5 | 本文件、`real-llm/result.json` + 17 张截图、`backup-drill.md`、`security-audit.md`、`slo.md`、`slo-chain.md` | 全量 test/static/e2e/eval、真实 LLM + Chrome、SLO 6/6、备份/恢复 + R33、安全审计 |

## 4. 缺陷 #1 复验（探索链路 limit 200→100）

- **根因与修复**：旧生产产物请求 `limit=200`，后端硬上限 1–100（`version/interfaces/httpapi/helpers.go`、`delivery/interfaces/httpapi/routes.go`）；修复提交 `586e1d3` 将前端常量改为 100 并拆分图层错误。`wave0/defect-verification.md` 记录旧/新 `BUILD_ID`、构建时间与真实 Chrome 复验。
- **W0 复验**：重建产物后真实 Chrome 断言 `exploration-error`/`exploration-degraded`/`chain-issues-degraded` 均为 0，`limit=200` 请求为 0，7 链路节点 + 2 问题节点；两张截图。
- **W1 复验**：`p8-exploration-chain` E2E 在真实 API/Chrome 上通过四类节点、问题 Sheet、异常过滤、截断与重连；200 节点 axe 无 serious/critical（`color-contrast` 因 jsdom 限制排除）。
- **W5 复验**：本次全量 E2E 的 P8 cross-wave 套件 **9 passed**（含 `p8-exploration-chain`）；真实 LLM + Chrome 流程中探索链路渲染 **72 节点、0 错误图层**（`real-llm-06-exploration-chain.png`）。
- 结论：**缺陷 #1 已在当前 HEAD 修复并通过三层复验**。

## 5. 真实 LLM + 真实 Chrome 全流程（Task 35 新增）

命令：`$env:EZTEST_RUN_REAL_LLM="1"; powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p8-llm.ps1`

- 脚本：`scripts/acceptance-p8-llm.ps1`（gate：`EZTEST_RUN_REAL_LLM=1` + 真实 `llm_*`；启动 API/Web/fixture/executor；重建独立 `eztest_p8_llm` 库；Chrome 为宿主已安装 Google Chrome）。
- 结果：**29/29 checks，0 failures，17 screenshots**，wall 322.1s；`real-llm/result.json` 含全部检查/ID/截图 SHA-256。
- 真实模型路径：澄清消息（84 字符回复）→ 设计运行 `done`（9 节点、7 个可执行用例）→ 7/7 baseline 用例 passed → 报告 revision 2 + Markdown 3772B → 问题/证据 → 待办/知识审批 → 全局搜索 4 组 → 视觉基线（inconclusive/not_run/missing_visual_baseline + 真实 executor 截图）→ 审批基线 → 增量批次 passed（`diffRatio=0`、`environmentMatch=true`、`visual_actual`/`visual_diff` 证据）→ 质量中心 6 指标。
- 浏览器断言：无 Next.js error overlay；全流程 **0 console error**；页面不渲染任何配置秘密（管理员密码/DB URL/LLM key/secret keys）；普通用户只读知识无审核控件且 0 console error。
- 证据截图：`real-llm/real-llm-01-overview.png` … `real-llm-17-knowledge-normal-user.png`（见 §6）。

## 6. 真机截图（提交到 `docs/acceptance/p8/real-llm/`）

| 截图 | 关键内容 |
|---|---|
| `real-llm-01-overview.png` | 真实 LLM 版本总览指标卡 |
| `real-llm-02-requirements-clarification.png` | 需求 Tab + 真实澄清对话 |
| `real-llm-03-mindmap.png` | 真实模型生成的脑图（7 个用例节点） |
| `real-llm-04-execution-results.png` | baseline 批次结果（7/7 passed） |
| `real-llm-05-execution-evidence.png` | 证据查看器 |
| `real-llm-06-exploration-chain.png` | 探索链路 72 节点、无错误图层 |
| `real-llm-07-issue-evidence.png` | 问题详情/引用（人工问题证据引用为 0，见 §9） |
| `real-llm-08-report.png` | 报告 revision 2、导出可用 |
| `real-llm-09-todo-centre.png` | 待办中心 review 分组 |
| `real-llm-10-knowledge-review.png` | 知识审核抽屉 |
| `real-llm-11-knowledge-approved.png` | 已批准知识列表 |
| `real-llm-12-global-search.png` | Ctrl+K 搜索 4 组结果 |
| `real-llm-13-visual-results.png` | 增量视觉断言行 passed |
| `real-llm-14-visual-actual.png` | `visual_actual` 证据查看器 |
| `real-llm-15-visual-diff.png` | `visual_diff` 证据查看器 |
| `real-llm-16-quality-centre.png` | 质量中心 6 指标/历史行 |
| `real-llm-17-knowledge-normal-user.png` | 普通用户只读知识 |

另有 W0/W1/W2/W3/W4 的 E2E 截图（`wave0/`、`wave1/`、`e2e/`），均为真实 API/PostgreSQL/Next.js/executor/Chrome 产物；fake LLM 仅驱动确定性 E2E 的 Agent 回合。

## 7. SLO

- 最新报告：`slo.md`（`2026-10-29T11:18:28Z`，提交 `1b457d2`），**6/6 measured，Overall PASSED**；`slo-metrics.json` 同步提交。
- 实测：总览首屏 API P95 **67.1 ms**（≤2500）、浏览器 navigation P95 **119.4 ms**（≤2500）、探索链路 P95 **95.9 ms**（≤2500，250 nodes/400 edges 断言通过）、code changes P95 **266.8 ms**（≤2000）、8 并发批次 **8/8 passed / observed max 8 / 0 errors**、分析非模型开销 **558.7 ms**（≤5000，wall 560.9 − 2 次模型调用 2.2）。
- 口径：本机单机测量，不是生产 SLA；8 并发与分析开销使用 fake LLM/timing proxy 的真实 executor/API 链路，未 mock executor；测量行数据留在 dev DB（一次性 e2e/acceptance 重置清理）。
- Task 35 复核了报告与 metrics；未修改 `scripts/slo.ps1`。本任务尝试再次启动测量 harness 时，宿主负载下启动脚本未完成，故最终以**最新已提交的真实测量报告**为准，不重写其数字。

## 8. 备份 / 恢复与安全

- 备份演练：`test-backup.ps1 -Keep` **174/174**；主恢复 DB 68 表行数一致、默认布局 58/58 文件 SHA-256 一致；安全拒绝/篡改检测/覆盖 round-trip/不稳定行数 advisory 均 PASS（详见 `backup-drill.md`，2026-10-29 fix round 1 的 175 断言记录与本次 174 断言因表/文件数量变化而计数不同，均 PASS）。
- **R33 应用 smoke（本任务补齐）**：用演练保留的恢复库 `eztest_backup_drill_20261029214731_test` 启动当前 API（临时端口 14333）：
  - `GET /healthz` → 200；
  - `GET /api/health` → `status=ok, db=ok`；
  - `GET /api/openapi.yaml` → 200（680,007 B）；
  - `POST /api/auth/login` → 恢复库管理员登录成功；
  - `GET /api/versions?limit=5` → 200/5 行。
  结果 5/5 PASS，`backup-app-smoke.json`；恢复库未删除，供复核。
- 安全：静态 32 PASS/2 SKIP，live 45 PASS/1 SKIP/0 FAIL；实时矩阵覆盖未认证 401、普通用户 admin 403、只读 200、临时用户创建/禁用清理与 logout；秘密扫描 2784 文件 0 发现，`.env` 未跟踪且被忽略；依赖许可 29 个直接依赖在 MIT/Apache-2.0 风格白名单内，`axe-core` 为 dev-only MPL-2.0 例外，`@xyflow/react` MIT。唯一 SKIP：`govulncheck` 未安装（Go 依赖漏洞未实测）。

## 9. 已知限制（不阻塞判定，但必须披露）

1. **Docker / Compose 未实跑**：本机无 Docker；仅 `scripts/validate-compose.mjs` 静态校验（4 服务/6 卷/健康检查/卷挂载/Dockerfile）。容器内行为、镜像构建、真实部署未验证。
2. **CI 未托管**：`.github/workflows/ci.yml` 与 `scripts/check-ci.ps1` 仅静态结构检查；没有在 GitHub Actions/其它 CI 上真实运行过。
3. **复合 `scripts/acceptance.ps1` 未跑绿**：本机外部高负载导致 Go 集成包 600s 超时和 3 个 lazy-load 前端组件测试超时；每个组成套件单独通过。另有磁盘写满导致 PostgreSQL 崩溃恢复的环境事件。**不把解释当作 PASS**；这是“PASS WITH LIMITS”的主要限制。
4. **真实 LLM 评测 `-RealLLM` 未运行**：确定性评测 7/7 指标 PASS；真实模型验收由 `acceptance-p8-llm.ps1` 的澄清/设计/执行/视觉全流程覆盖（29/29），但 `eval.ps1 -RealLLM` 的语料级真实报告未生成。
5. **SLO 为本地口径**：6/6 本机测量，不是生产 SLA；8 并发与分析开销使用 fake LLM/timing proxy；未做生产数据量/多机复测。
6. **GC 仅证据文件（R39）**：自动 GC 只清理 `EZTEST_EVIDENCE_DIR` 下的证据对象；导出、附件、报告 Markdown、孤儿文件不自动清理（P9/后续）。
7. **`govulncheck` SKIP**：Go 依赖漏洞未实测；安装后需重跑。
8. **视觉 E2E 的 Agent 回合由 fake LLM 驱动**：真实 executor/真实 Chrome/像素比对/证据/分类未 mock；真实模型视觉路径已由本次 `acceptance-p8-llm.ps1` 覆盖（基线审批 + 增量 actual/diff），但只是一次真实模型运行，不是模型矩阵。
9. **人工问题的证据引用为 0**：产品 `CreateHuman` 只写 case_run/case_node/requirement 引用，不写 evidence ref；本次问题详情截图显示证据引用 0，执行证据查看器单独验证（`real-llm-05`）。这是当前产品语义/限制，不是缺陷掩盖。
10. **CSP 为 report-only**：Next 安全头已配置，但 CSP 不阻断内联脚本/样式；生产 enforcing 前需 nonce/hash 迁移。
11. **token 在 sessionStorage**、无 HttpOnly 会话；TLS/反代/HSTS 由部署方负责；API 响应头不被 Next 配置覆盖。
12. **备份无 PITR/WAL 归档**；RPO 为最近一次成功备份；生产 RTO 未按真实数据量演练。
13. **Windows + 单机 Chrome**：E2E/SLO/真实 LLM 均为 Windows 本机、Chrome channel；未覆盖 Edge/Firefox/CI Linux 浏览器矩阵。
14. **executor 固定端口与单机资源**：8 并发测量依赖 `EZTEST_EXEC_MAX_CONCURRENT_CASES=8`；本次真实 LLM 验收使用 1 并发以复用单 fixture 会话；未做多 executor 水平扩展验证。

## 10. 可复现命令

```powershell
# 1) 单元/集成/前端/executor
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1

# 2) 静态验收
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly

# 3) 全量 E2E（workers=1 由 playwright.config.ts 固定）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1

# 4) 确定性评测
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1

# 5) 真实 LLM + 真实 Chrome（gated）
$env:EZTEST_RUN_REAL_LLM = "1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p8-llm.ps1 -DryRun
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p8-llm.ps1

# 6) SLO（最新报告）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun
# 真实测量需要 API/Web/executor + loopback fake LLM timing proxy：
# powershell.exe ... scripts/slo.ps1 -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://localhost:6868 -LlmCallLogPath .tmp/p8-slo/llm-calls.jsonl

# 7) 备份恢复 + 保留恢复库
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test-backup.ps1 -Keep

# 8) 安全审计（启动 API 后 live；否则 -SkipLive）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1
```

## 11. 证据索引（提交文件）

- `docs/acceptance/p8/acceptance.md`（本文件）
- `docs/acceptance/p8/real-llm/result.json` + `real-llm/real-llm-01..17*.png`
- `docs/acceptance/p8/wave0/defect-verification.md` + 2 PNG
- `docs/acceptance/p8/wave1/exploration-chain.md`、`react-flow-spike.md` + 6 PNG
- `docs/acceptance/p8/e2e/README.md` + 22 PNG
- `docs/acceptance/p8/backup-drill.md`
- `docs/acceptance/p8/security-audit.md`
- `docs/acceptance/p8/slo.md`、`slo-metrics.json`
- `docs/acceptance/p8/slo-chain.md`、`slo-chain-metrics.json`

本地（gitignored）：
- `.tmp/p8-final-20261029-192239/`：test/static/e2e/eval/backup/security/real-llm 日志与 smoke JSON
- `.tmp/p8-final-llm-20261029-210545/`：真实 LLM 运行的 API/Web/executor/browser 日志
- `.tmp/p8-backup/`：备份/恢复 manifest、report、恢复目录
- `.tmp/p8-eval/`：评测结果/观测数据

## 12. 结论

P8 的 P0–P8 功能、跨波 UI/API、真实 LLM + 真实 Chrome 全流程、确定性评测、SLO、备份恢复（含恢复库应用 smoke）与安全审计均已取得真实证据；缺陷 #1 已修复并三层复验。唯一未闭环的是**复合 acceptance 脚本在本机外部负载下的计时类失败**，以及 Docker/CI/真实 LLM 语料级评测等固有边界。因此判定为 **PASS WITH LIMITS**，不宣称“全量门禁全绿”。
