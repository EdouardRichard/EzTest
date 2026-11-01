# Phase 06 Acceptance — 质量/搜索/待办/Agent 中心/全局中心/链路 v2

> 阶段：P6（`phase-06-parity-quality-search-agent-center.md`）
> 分支：`feat/v0.2-p6`；日期：2026-11-01
> 验收口径：用户裁决 **全程真实 LLM + 真实 Chrome，不使用 fake-LLM**；真实 LLM 评测阈值仅报告轨，不阻断阶段。
> 数据来源：真实 P4 验收数据库 `eztest_v02_p4`（2 版本 / 8 case_run / 2 问题 / 7 报告 / 真实 agent_run）克隆为 `eztest_v02_p6`，P6 API 应用 #32/#33（33 迁移 / 72 设置）。

## 1. 环境与命令

```powershell
# 1) 隔离库 + 33 迁移 + 72 设置 + 真实 DeepSeek 连通 + S0-S9
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p6-acceptance.ps1

# 2) 真实浏览器 E2E（真实 API/DB，无 fake 上游）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p6-e2e.ps1

# 3) 契约门禁
node scripts/p2-contract-check.cjs; node scripts/p3-contract-check.cjs
node scripts/p4-contract-check.cjs; node scripts/p5-contract-check.cjs
node scripts/p6-contract-check.cjs

# 4) 全量测试（P6 收口）
cd backend; ..\.tools\go\bin\go.exe test -count=1 ./...
cd ..\frontend; pnpm test; pnpm typecheck; pnpm lint
cd ..\executor; pnpm test
```

## 2. P6 验收结果（S0–S9，真实 LLM/Chrome）

证据：`.tmp/p6-acceptance/scenarios.txt`、`acceptance.log`、`api.log`、`api.err.log`、`.tmp/p6-eval/result.json`、截图目录。

| 场景 | 结果 | 证据摘要 |
|---|---|---|
| S0 门禁/健康 | PASS | health=ok；migrations=33；settings=72；真实 LLM config test=200 |
| S1 质量中心/评测权限 | PASS | overview 7 运营指标；visual-metrics=200；普通用户 eval-runs=403 |
| S2 全局搜索 | PASS | 5 组；total=14；空 q=400 |
| S3 待办 | PASS | admin items=9（含 6 dead）；普通用户 dead=0 |
| S4 Agent 中心/提示词 | PASS | 14 profiles；runs=200；prompt 版本/预览/回退；真实澄清 run 的 `context_packet` 含新 prompt token（next-run 生效） |
| S5 全局问题/报告 | PASS | issues=2；reports=7；跨版本只读 |
| S6 链路 v2 | PASS | q/abnormal/severity 过滤后 nodes=4；非法枚举=400；counts 扩展 |
| S7 真实 LLM 评测 | PASS | `eval.ps1 -Mode real-llm` exit 0；`eval_run` 6 指标落库；阈值/基线报告轨（status=failed 不阻断） |
| S8 P5 回归 | PASS | P5 `visual-metrics`=200；版本 `visual/overview`=200 |
| S9 契约门禁 | PASS | P2–P6 契约脚本全 0 |

## 3. 浏览器 E2E（真实 API/DB）

`scripts/p6-e2e.ps1`：**2 passed (5.4s)**，真实 Chrome；无 fake 上游。

截图（`docs/version-V0.2/acceptance/p6/screenshots/`）：

- `p6-01-quality-admin.png`（admin 质量中心/评测看板）
- `p6-02-agents-admin.png`（Agent 中心/配置）
- `p6-03-issues-admin.png`、`p6-04-reports-admin.png`（全局中心）
- `p6-05-chain-admin.png`（链路 v2）
- `p6-06-search-results.png`（⌘K 搜索）
- `p6-07-todos-admin.png`（全局待办）
- `p6-08-quality-user.png`、`p6-09-agents-user.png`（普通用户只读/无 prompt）

## 4. 测试与门禁

| 命令 | 结果 |
|---|---|
| `go test -count=1 ./...` | PASS（全部后端包） |
| `pnpm test` | **97 files / 739 tests passed** |
| `pnpm typecheck` / `pnpm lint` | exit 0（lint 1 个既有 warning） |
| `cd executor; pnpm test` | PASS（104/104） |
| P2–P6 契约 | 全 PASS；P6：20 ops / 18 path items / 3 tables / #32–#33 / 72 settings |
| 迁移幂等 | 33 条；重启 0 应用（P4 模板克隆后首次应用 33） |
| 真实 LLM 评测 | `eval.ps1 -Mode real-llm` exit 0；result 6 指标；report-only |

## 5. 已知限制/偏差

1. **数据源**：S0–S9 使用 P4 真实验收数据库克隆（`eztest_v02_p4` → `eztest_v02_p6`），而非重跑完整 P3/P4 闭环；这是为复用真实 LLM/Chrome 数据、避免重复长流程。P6 新增读模型全部读取真实表/真实 agent_run。
2. **视觉数据**：P4 数据库没有 P5 视觉断言数据，S8 只验证 P5 视觉读端点 200；P5 视觉专项证据见 `phase-05-handoff.md`（13/13 PASS）。
3. **评测阈值**：真实 LLM 报告轨 status=failed（reproduced sample：design 0.8、impact 0、classification 0、issue FP 1.0、supplement 0）；阈值/基线为观察信号，不阻断（用户 U4/P6-D-12/P6-D-21）。
4. **E2E 范围**：浏览器 E2E 覆盖页面/权限/搜索/待办/链路/配置可见性，不重复触发真实 LLM agent 流程；真实 LLM next-run 由 S4 的 `context_packet` 断言覆盖。
5. **无 fake-LLM**：验收/评测/E2E/演示未使用 fake；单测/集成测试的离线替身只验证逻辑，不作为验收证据。

## 6. 运行产物

- `.tmp/p6-acceptance/scenarios.txt`（S0–S9）
- `.tmp/p6-acceptance/acceptance.log`
- `.tmp/p6-acceptance/observation.json`（真实数据 observation）
- `.tmp/p6-eval/result.json` / `observation.json` / `eval.log`
- `.tmp/p6-acceptance/screenshots/`（9 张）
- `docs/version-V0.2/acceptance/p6/screenshots/`（提交版截图）
