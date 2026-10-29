# EzTest 评测语料（P8 §9.1 / D61）

本目录是**评测语料与基线**的唯一来源。评测脚本 `scripts/eval.ps1` 只通过 admin Bearer API
写入 `quality.eval_run` / `quality.eval_metric`，不直接写业务表；任何语料/真值变更都必须先改这里，
再更新 `baseline.json` 并在 PR 中说明。

## 双轨

| 轨道 | 触发 | 作用 |
|---|---|---|
| 确定性（默认） | `powershell.exe -File scripts/eval.ps1` | 固定语料 + fake LLM + fixture executor；阈值与基线回归是硬门禁，失败退出码非 0。 |
| 真实 LLM 报告 | `powershell.exe -File scripts/eval.ps1 -RealLLM`（需 `EZTEST_RUN_REAL_LLM=1`） | 真实模型报告，不进入硬门禁；结果只作为质量观察记录。 |
| 静态自检 | `powershell.exe -File scripts/eval.ps1 -DryRun` | 校验语料/基线/工具链/DB 目标，不启动服务、不调用 LLM。 |

`-DryRun` 也是 `scripts/acceptance.ps1 -StaticOnly` 的挂钩：语料或基线缺失、JSON 无法解析、
数据库目标不安全时整体失败。

## 目录

```
eval/
├── README.md
├── baseline.json                  # 上次确定性通过结果的冻结基线（提交入库）
├── corpus/
│   ├── requirements.json          # 固定需求、版本、期望的用例标题契约
│   ├── environment.json           # 固定环境/账号/端口/评测库名
│   └── visual.json                # 视觉断言场景（P8 §9.3）
└── ground-truth/
    ├── impact.json                # P4 影响候选真值（固定 commit 对）
    ├── classification.json        # 注入失败分类真值
    ├── issues.json                # AI 问题真值（标题稳定契约）
    ├── supplement.json            # 补全缺口真值
    └── visual.json                # 视觉断言四态分类真值（P8 §9.3）
```

## 指标与阈值（§9.1）

| 指标 | 定义 | 确定性阈值 | 真实 LLM 报告阈值 |
|---|---|---|---|
| `design_coverage` | 已覆盖已确认需求 / 已确认需求 | ≥ 0.90 | ≥ 0.80 |
| `impact_recall` | 命中真值用例 / 真值总数 | ≥ 0.90 | ≥ 0.70 |
| `impact_precision` | 命中真值用例 / P4 选中的需求候选 | ≥ 0.80 | ≥ 0.60（报告口径） |
| `classification_accuracy` | 分类正确失败 / 注入失败 | = 1.00 | ≥ 0.80 |
| `issue_false_positive_rate` | 无真值 AI 问题 / AI 问题总数 | ≤ 0.05 | ≤ 0.15 |
| `supplement_hit_rate` | 命中缺口的补充用例 / 补充用例 | ≥ 0.80 | ≥ 0.50 |

回归规则：确定性轨道任一指标比 `baseline.json` 差超过 **0.05** 即失败（问题误报率为“升高”方向）。
真实 LLM 轨道只报告，不应用回归规则。

## 视觉断言场景（P8 §9.3）

`corpus/visual.json` 冻结视觉断言的场景（选择器/区域/阈值/视口/DPR、基线审批与
`visual_actual`/`visual_diff` 证据），`ground-truth/visual.json` 冻结四态分类真值：
无基线 → `inconclusive(case, missing_visual_baseline)`；环境指纹不符 →
`inconclusive(environment, visual_environment_mismatch)`；diff 超阈值 →
`failed(application, visual_mismatch)`；否则 passed。

Task 23 的 runner 目前尚未执行该场景（不修改 runner 核心）。接入时需在预检的
`requiredFiles`、`corpusFiles` 与 `P8_EVAL_*` 环境变量中加入这两个文件，并按
`ground-truth/visual.json` 的 `cases` 计数（`visual_classification_accuracy`，阈值 1.00）。

## 维护流程

1. **先冻结语料**：新增/修改需求、环境、真值时同步提升 `corpusVersion`（当前 `2026-10-29.1`）。
2. **标题是契约**：确定性 fake LLM（`scripts/fake-llm.cjs`）的用例/问题/补全标题是评测锚点；
   修改 fake LLM 输出必须同步本目录的真值并提升 `corpusVersion`。
3. **更新基线**：确定性门禁通过后，把 `.tmp/p8-eval/result.json` 中的指标写入
   `baseline.json`（只允许“不下降”的更新），并在提交说明中附上运行命令与结果。
4. **真实 LLM**：`-RealLLM` 需要 `.env` 的 `llm_*` 配置与 `EZTEST_RUN_REAL_LLM=1`；结果写入
   `.tmp/p8-eval/real-llm-report.json`，低于报告阈值时记录为“质量观察”，不阻断发布。
5. **不得**把密钥、prompt 或证据正文写进语料；`eval_metric.detail` 只存计数与判定理由。

## 确定性链路（runner 做哪些事）

`scripts/eval.ps1` 准备独立 `eztest_eval` 库、构建并启动 API、fake LLM、fixture target 与
Playwright executor，然后按阶段执行：创建版本/需求 → 澄清确认 → 设计（fake LLM）→ 评审/基线冻结
→ P4 索引/影响分析 → 基线批次执行（注入 application 失败）→ 失败分析/问题整理 → 补全运行/补全批次
→ 报告；最后按本目录真值计算指标、POST 到 `/api/quality/eval-runs`、比较基线并输出
`.tmp/p8-eval/{observation,result}.json`。

失败时脚本保留 `.tmp/p8-eval/api.out.log`/`api.err.log`、`fake-llm.out.log`/`fake-llm.err.log`、
`executor.out.log`/`executor.err.log`、`fixture.out.log`/`fixture.err.log`、`runner.log` 与
`.tmp/p8-eval/observation.json` 便于复盘；不会打印 `.env` 中的密钥或 LLM key。
