# P8 跨波 E2E 验收证据（Task 34）

本目录保存 P8 跨波浏览器回归（Task 14–33 的最终 UI/API）在真实 API、真实
PostgreSQL、真实 Next.js、真实 Playwright executor、真实 Chrome 与确定性 fake
LLM/fixture 上运行产生的截图证据。

Fix round 1 补充（2026-10-29）：跨账号 console 监控改为在每个阶段边界断言后再切换
（并加了「早期阶段注入 console error 必须失败」的回归步骤）；`/knowledge` 首次加载
在深链参数就绪前不再发全量请求；`/quality` 的评测记录改为 seeded
`mode=deterministic` fixture（不再是 real_llm 报告）；fake LLM 的四态视觉判定补齐
P6 契约测试。

## 运行方式

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 `
  p8-shell p8-version-detail p8-management p8-quality
```

脚本会重建 `eztest_e2e` 数据库、构建 API、启动 fake LLM（`:13334`）、fixture
target（`:13335`）、真实 executor（`:13336`）和 Web（`:6868`），然后按参数顺序
运行 4 个 spec；Playwright 保持 `workers: 1`。

## 用例矩阵

### `frontend/e2e/p8-shell.spec.ts`（1 个串行场景）

| 用例 | 断言要点 | 证据 |
|---|---|---|
| 版本切换器保留 Tab/批次 | 顶栏显示当前版本名/状态；本地搜索过滤；切换使用客户端路由；URL 保留 `tab=exploration&batch=<id>`；目标版本无批次时批次选择器消失、切回执行版本后 `exec-batch-selector` 重新选中该 batch 且 `batch-kind-badge` 为 `baseline`；双向切换；`eztest.recentVersions` 最近列表顺序 | `p8-shell-01-version-switcher.png` |
| 待办中心分组/深链 | 管理员 review 分组同时包含 reviewing 版本与知识草稿；组计数 ≥2；无 takeover/gate 组；版本深链到 `?tab=mindmap`，知识深链到 `/knowledge?review=<id>` 并自动打开审核抽屉 | `p8-shell-02-todo-review.png` |
| 全局搜索 | `Ctrl+K` 打开命令面板；真实 API 结果分 version/requirement/case/issue 4 组；`Enter` 打开第一条深链；`?tab=overview` 生效；最近搜索；`Esc` 关闭；`/` 再次打开 | `p8-shell-03-global-search.png` |
| 375/768 视口导航 | 汉堡可见、桌面侧栏隐藏；抽屉打开/`Esc`/遮罩/关闭按钮/路由跳转均关闭；无横向溢出；375 与 768 两档 | `p8-shell-04-mobile-nav-375.png`、`p8-shell-05-tablet-nav-768.png` |

数据 fixture：一个真实执行版本 + 一个真实批次（用于 `batch` 参数）、一个
reviewing 版本（含需求/场景/用例/人工问题/知识草稿）。搜索/待办/知识 fixture
在 `finally` 中通过 psql 删除。

### `frontend/e2e/p8-version-detail.spec.ts`（3 个场景）

| 用例 | 断言要点 | 证据 |
|---|---|---|
| 脑图 Tab 与全屏路由一致 | `?tab=mindmap&case=` 渲染嵌入版（`data-embedded=true`、无全屏返回/工具栏），全屏 `/versions/{id}/mindmap?case=` 选中同一用例且保留返回链接；非法 `case` 显示提示不崩溃 | `p8-version-detail-01-mindmap-tab.png`、`p8-version-detail-02-mindmap-fullscreen.png` |
| 代码与变更批次时间线/受影响用例 | 真实 P4 fixture 仓（base→head）索引 + 分析；`GET /code/changes` 返回 `batchTimeline`；UI 显示 Batch 1、commit 对、变更文件 7、影响用例 1、`用例 <caseNodeId>`、`src/api/refund.ts` | `p8-version-detail-03-code-timeline.png` |
| 报告 revision 切换/stale | 真实批次完成后报告 revision 1；人工问题改变输入指纹后 stale 横幅出现；UI 重新生成 revision 2；下拉切回 revision 1 再次显示 stale，切回 revision 2 消失 | `p8-version-detail-04-report-stale.png`、`p8-version-detail-05-report-revision.png` |

报告 fixture 的 `report` 行在 `finally` 中删除，不污染全局报告列表；执行版本与
代码分析等重 fixture 留在一次性 e2e 库（每次 `e2e.ps1` 重建）。

### `frontend/e2e/p8-management.spec.ts`（1 个场景）

| 用例 | 断言要点 | 证据 |
|---|---|---|
| Agent 中心 admin 可配 / 普通用户只读 | 13 角色目录；admin 有「配置与控制台」与「编辑」入口并进入 Agent 运行控制台；普通用户有 `readonly-banner`、无上述入口；普通用户 catalog 200、PATCH profile 403 `forbidden` | `p8-management-01-agents-admin.png`、`p8-management-02-agents-readonly.png` |
| 审计页筛选 | 真实 `version.created` 审计；按 actor + action 过滤；改用非匹配 actor（新建普通用户）后同一 action 显示显式空态且目标版本行消失，再恢复 admin actor；打开记录详情；无匹配 action 时显式空态；重置恢复 | `p8-management-03-audit-filter.png` |
| 基础配置/GC | 只读投影（环境/限流/SLO/存储/证据 GC），面板内无 input/select/textarea；GC dry-run 显示候选/受保护/dry-run 结果；执行按钮在有候选时才可点并要求 alertdialog 二次确认（本 fixture 无超期候选时断言禁用） | `p8-management-05-basic-config.png`；若出现候选则另有 `p8-management-04-gc-confirm.png` |
| 普通用户设置 | 仅主题面板，无 admin Tab | — |

### `frontend/e2e/p8-quality.spec.ts`（3 个场景）

| 用例 | 断言要点 | 证据 |
|---|---|---|
| 知识草稿 → admin 审批 → `/knowledge` 可见 | 普通用户在版本总览提交草稿（真实 UI）；admin 在 `/knowledge?review=` 审核抽屉批准；API 状态 approved；admin 与普通用户都可在全局只读列表看到 | `p8-quality-01-knowledge-draft.png`、`p8-quality-02-knowledge-approved.png`、`p8-quality-03-knowledge-visible.png` |
| 视觉断言基线审批 → 真实执行 → 证据 | 见下节“视觉真实路径” | `p8-quality-04-visual-results.png`、`p8-quality-05-visual-actual.png`、`p8-quality-06-visual-diff.png` |
| `/quality` 权限与指标 | admin 通过 API 记录一次 seeded **deterministic fixture** 评测（`mode=deterministic`、`model=deterministic-fixture`、完整 baseline，非真实 LLM 报告），看板显示最近 run、`确定性门禁` 轨道、6 项指标与历史行；普通用户看到 `quality-page-denied`，直接 API 403 `forbidden` | `p8-quality-07-quality-admin.png`、`p8-quality-08-quality-denied.png` |

## 跨账号 console 监控

管理面、知识沉淀与质量中心都会在同一个用例内切换账号。监控器现在在
`switchAccount` **停止旧 monitor 之前**先断言该阶段没有 console error / pageerror /
Next.js error overlay，再关闭 monitor、登出、登录并开启下一个 monitor；最后一个阶段
仍由收尾的 `expectNoConsoleErrors` 断言。因此每个阶段（admin Agent 中心、普通用户
只读、admin 审计/基础配置、普通用户设置、知识草稿、审批、普通用户读取、质量看板、
质量拒绝页）都各自被覆盖，而不是只检查最后一个账号。

登出重定向期间 in-flight shell 请求携带刚清除的 token 可能产生一次 401 资源错误；
该过渡窗口刻意处于 monitor 关闭状态，所以不需要宽泛地忽略 401。管理面用例还包含一个
回归步骤：在早期阶段显式 `console.error` 一个标记，要求 `switchAccount` 在登出前
拒绝并抛出包含该标记的断言（旧实现会静默丢掉这个错误）。

## 视觉真实路径（D7 / §9.3）

1. 通过真实 Mindmap API 创建 `kind=visual` 断言并配置全页、`1280×720`、DPR `1`、
   阈值 `0.05`；随后走真实的 submit-review + approve，冻结用例基线。
2. 从版本详情 UI 点击「开始执行」，真实 Playwright executor 在 fixture target
   上执行基线批次：
   - `execution.visual_assert` 记录 `not_run`，分类
     `inconclusive(case, missing_visual_baseline)`；
   - 用例证据包含真实 `screenshot`（executor 截图），不含 `visual_actual`。
3. 管理员通过真实 API `POST /api/versions/{id}/mindmap/assertions/{aid}/visual/baseline`
   把该次运行的截图证据审批为视觉基线（审计/事件 `case.visual_baseline_approved`）。
4. 完成版本新增一个需求 → continuing → designing，创建真实的 **incremental
   batch** 重新执行同一用例（这是基线冻结后再次执行该用例的产品内路径）。
5. 第二次执行由真实 executor 在目标页截取 actual 并用 `pixelmatch` 比对：
   - 分类 `passed`（`diffRatio ≤ threshold`、`environmentMatch=true`）；
   - 证据落库为 `visual_actual` 与 `visual_diff`（PNG，sha256 与响应头一致）；
   - 结果页可打开证据查看器，分别显示「视觉实际截图」「视觉差异图」。

## 为通过验收做的必要修复（均在报告中说明）

1. `backend/internal/mindmap/application/visual_assertion_service.go`：
   `ApproveBaseline` 不再要求版本处于 designing/reviewing 且无冻结基线。视觉基线
   候选只能来自执行截图，而执行必然发生在用例基线冻结之后；原状态门使视觉断言
   永远无法获得已审批基线（P0 功能缺陷）。证据归属/kind/PNG/尺寸/上限校验保留。
2. `frontend/src/features/knowledge/KnowledgePage.tsx`：`?version=`/`?review=`
   深链改在 mount effect 中读取。Next App Router 在首个客户端导航渲染后才
   `pushState`，渲染期读取 `window.location` 会丢失待办中心的知识审核深链
   （`?review=`）与版本筛选深链（`?version=`）。Fix round 1 增加 `paramsReady`：
   深链参数就绪前 load effect 不发送任何请求，首次请求即携带 `versionId`；
   组件测试覆盖「首次请求不得无过滤」与「延迟的全量响应不能覆盖已过滤 entries」。
3. `scripts/fake-llm.cjs`：确定性执行 Agent 对 `kind=visual` 断言调用
   `execution.visual_assert`，并按服务端结果生成
   `inconclusive(case)/inconclusive(environment)/failed(application)/passed` 的
   finish。像素比较、证据、分类均由真实 API/executor 完成，未 mock。
   `scripts/fake-llm.p6.test.cjs` 新增四态契约测试（含 environment-mismatch 与
   failed），并验证 wire 上只传 `assertionId`。

## 已知限制

- **执行 Agent 由 fake LLM 驱动**（P6/P7 同样做法），视觉工具调用因此可确定地
  覆盖四态；真实 LLM 不在 E2E 门禁内。真实 executor/像素比对/证据/分类未 mock。
- **`/quality` 的评测记录是 seeded deterministic fixture**（`mode=deterministic`，
  完整 baseline + 固定 sample），用于验证看板/权限链路；它不是真实模型评测报告，
  UI 与证据中显示为「确定性门禁」。
- **视觉基线审批使用 admin API**（与 UI「审批为基线」同一端点）：版本完成后脑图
  只读，UI 审批入口不可达；任务允许使用 admin API 上传/审批截图证据。
- **GC 确认对话框**仅在 dry-run 候选 > 0 时出现。本 e2e 库中的执行证据均为近期
  或受保护证据，通常候选为 0，因此用例断言「执行 GC」禁用；候选 > 0 时用例会
  点击并断言二次确认后取消。
- **takeover/gate 待办分组**需要 blocked(session) case run 才会出现；shell 用例
  覆盖 review 分组与版本/知识深链，takeover/gate 的采集由 P6/P7 执行类 spec 覆盖，
  本任务未在 shell 场景重复造 takeover fixture。
- 执行版本/批次等重 fixture 留在一次性 `eztest_e2e` 库中；`scripts/e2e.ps1`
  每次运行都会重建该库，仓库不保留数据。
