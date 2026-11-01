# EzTest 0.2 P6 真实 LLM 评测语料（`eval/v02`）

> 状态：P6 报告轨（spec §5.3/§5.4、P6-D-12/P6-D-21）。
> **只做真实 LLM 评测**：不允许 fake-LLM / deterministic 模式；阈值与基线只报告、不阻断阶段验收。
> 真实 LLM 失败按系统缺陷定位（上下文包/工具 scope/schema/提示词/服务端校验/超时预算），修复后重跑。

## 目录

| 路径 | 作用 |
|---|---|
| `baseline.json` | `corpus_version` + 7 指标初始基线/阈值；只允许“不变差”更新 |
| `corpus/environment.json` | 固定环境/账号/模型坐标（非敏感） |
| `corpus/requirements.json` | 4 条需求与期望确认/覆盖 |
| `corpus/design.json` | 场景/用例与期望确认清单 |
| `corpus/code.json` | 固定仓库、变更文件、代码锚点与期望影响需求 |
| `corpus/execution.json` | 固定波次、用例运行状态、注入失败真值 |
| `corpus/issues.json` | 期望 AI 问题与真阳性场景 |
| `corpus/visual.json` | 视觉断言与期望分类 |
| `ground-truth/impact.json` | 影响召回/精确率的真值需求集合 |
| `ground-truth/classification.json` | 注入失败及期望 failure class |
| `ground-truth/issues.json` | AI 问题真阳性标题 |
| `ground-truth/supplement.json` | 期望 AI 补充用例 |
| `ground-truth/visual.json` | 视觉分类真值 |
| `observation.example.json` | 真实 run 的 observation **契约模板**（不是证据，本身不可用于提交评测） |

所有文件共享同一个 `corpus_version`（当前 `v02-2026-11-01.1`）。修改任一语料必须同时 bump 全部文件的 `corpus_version`，否则 `scripts/eval.ps1 -DryRun` 会失败。

## 真实 LLM 数据来源（observation）

P6 只从 **真实验收数据** 计算样本，不接受手工统计的指标。P6 验收流程（真实 DeepSeek `deepseek-flash` + 真实 Chrome）产出 observation JSON，`scripts/eval.ps1 -Mode real-llm` 读取它、按提交的 ground-truth 计算 sample 计数，再由服务端派生 6+1 指标并落库。

observation 契约（`schema: p6-eval-observation/v1`）见 `observation.example.json`：

- `mode` 必须为 `real_llm`；`model` 必须为真实模型名（如 `deepseek-flash`，含 `fake` 直接失败）。
- `corpus_version` 必须与语料一致。
- `version_id` 为真实 P6 版本 ID。
- `requirements.confirmed_keys`：真实确认的需求 key。
- `design.covered_requirement_keys`：真实设计覆盖的需求 key。
- `impact.selected_requirement_keys`：真实代码影响分析选中的需求 key。
- `execution.case_runs[]`：真实执行结果（`case_title/status/failure_class`）。
- `issues.ai_issues[]`：真实 AI 问题（`title`）。
- `supplement.ai_case_titles[]`：真实 AI 补充用例标题。
- `visual`（可选，有视觉断言时提供）：`injected` 与 `cases[{case_title,classification}]`。

脚本只信任 open-truth 匹配后的 **计数**；不会把 observation 里的 `sample` 字段当真（契约里没有该字段）。样本物理约束在脚本与服务端各校验一次。

## 命令

```powershell
# 静态检查：语料/基线/ground-truth 一致性、OpenAPI/路由、DB 目标、工具链；不调用 LLM
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -DryRun

# 真实 LLM 报告轨：读取真实 observation → 计算 sample → POST /api/quality/eval-runs
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -Mode real-llm `
  -ApiBaseUrl http://127.0.0.1:14345 `
  -ObservationFile .tmp/p6-acceptance/observation.json
```

参数/环境变量：

| 参数 | 默认 | 说明 |
|---|---|---|
| `-Mode` | `real-llm` | 唯一合法值；不存在 fake/deterministic |
| `-DryRun` | off | 只做静态/DB/工具链检查 |
| `-ApiBaseUrl` | `EZTEST_API_BASE_URL`，否则 `http://127.0.0.1:<EZTEST_API_PORT\|13333>` | P6 验收用隔离端口 |
| `-ObservationFile` | `EZTEST_EVAL_OBSERVATION`，否则 `.tmp/p6-acceptance/observation.json` | 真实 observation |
| `-BearerToken` | `EZTEST_ADMIN_TOKEN`；否则用 `.env` 的 `EZTEST_ADMIN_USERNAME`/`EZTEST_ADMIN_PASSWORD`（缺省为开发 seed `admin`/`admin123`）登录 | admin 凭据/口令永不打印 |
| `-RequestTimeoutSeconds` | 60 | 单次 HTTP 超时 |

DryRun 校验：14 个语料/真值文件存在且 JSON 可解析、`corpus_version` 一致、baseline 7 指标与阈值精确匹配 spec §5.3、语料与真值互相引用一致、OpenAPI 冻结标记/P6 quality 路径与 schema、quality 路由文件、迁移 `order.txt` 33 条（含 `quality_0001_eval.sql`）、`.env` 数据库 URL 本地可连接、LLM 配置三项非空、工具链（PowerShell/psql）。不要求服务已启动。

## 输出/退出码

- 成功写 `.tmp/p6-eval/result.json`（run id/status/metrics/regressions/sample/baseline/报告轨说明）和 `.tmp/p6-eval/observation.json`（真实 observation 存档）。
- **阈值/基线失败不是脚本失败**：`report_only=true`，退出码 0，回归项列在 `result.regressions`。
- API/DB/observation/鉴权失败：写 `result.json` 错误详情并退出 1；日志保留在 `.tmp/p6-eval/`。
- `.tmp/` 是本地运行产物，不提交；验收结论引用其路径。

## 基线更新

1. 第一次真实 LLM run 完成后，用 `result.json.metrics` 更新 `baseline.json.metrics`；只允许不变差（下界指标不下降，`issue_false_positive_rate` 不上升）。
2. 语料/真值改动必须 bump `corpus_version` 并重新跑真实验收；不得为了让指标好看而下调阈值或伪造 observation。
3. 阈值是报告信号，不是阶段门禁；不改 `docs/version-V0.2/` 设计文档。
