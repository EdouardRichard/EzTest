# P8 Task 13：探索链路 E2E / 可访问性 / 200 节点性能验收

- 验收时间：2026-10-29 16:19–16:26 +08:00
- 仓库/分支：`D:\Project_new\EzTest` / `dev`
- 基线 HEAD（开始时）：`65f1787`；验收期间并行任务提交推进到 `f3e3a9b`（`feat(p8): add evidence gc with reference protection`）。本任务未修改这些并行任务的文件。
- 验收范围：真实后端 + 真实 Chrome 的探索链路 E2E、200 节点 axe/键盘门禁、200 节点/400 边 P95。
- 结论：**全部通过**。E2E 1 passed；axe 无 serious/critical；链路 API P95 85.1 ms（阈值 800 ms）；画布可交互 P95 1130 ms（阈值 2500 ms）。

## 1. 探索链路 E2E

命令（仓库根目录，自动重置并启动 `eztest_e2e` API/Web/Fake LLM/Executor/Fixture）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p8-exploration-chain
```

结果：`1 passed (22.5s)`，`==> E2E PASSED`，退出码 `0`。真实数据由 `seedP7ExecutionVersion` 播种：一个失败基线批次触发 P7 失败分析/问题整理，生成带证据的 AI 问题；补全批次再执行两条通过的 AI 补全用例，因此正常/异常结果同时存在。

| 验收项 | 实际断言 | 结果 |
| --- | --- | --- |
| 打开 `?tab=exploration` | `exploration-tab` 可见 | PASS |
| 无错误条 | `exploration-error` / `exploration-degraded` / `chain-issues-degraded` 计数均为 0 | PASS |
| 四类节点 | 从真实 API 快照取 requirement/case/result/issue 各一，节点与卡片均可见 | PASS |
| 可见计数 | UI「可见 N 个节点」= 读模型 `nodes.length` | PASS |
| 问题节点 Sheet | 证据区、`加载证据（N）`、加载后的证据按钮、「查看问题」深链均可见 | PASS |
| 问题深链 | 点击「查看问题」→ `issues` Tab 打开且 `issue-detail` 为同一问题 id | PASS |
| 折叠/展开计数 | 折叠后可见数下降且文案含「折叠 M 个」；展开后恢复到折叠前数值 | PASS |
| 仅异常 | 勾选后 URL 带 `abnormal=1`，可见数等于异常读模型节点数；通过的补全结果节点为 0 个 | PASS |
| 断线模拟 | 拦截 `/api/versions/*/stream*` 后 `chain-stream-status` 显示「重连中」，同时画布节点仍渲染 | PASS |
| >100 需求 fixture | psql 播种 101 条需求后显示 `已显示前 100 条（共 101 条）` 与「需求层已截断」 | PASS |
| console/秘密 | 除被拦截的 SSE 传输外无 console/page error；管理员与 fixture 密码均未出现在页面文本 | PASS |

### 截图

| 文件 | 尺寸 | 字节 | SHA-256 |
| --- | --- | --- | --- |
| `p8-chain-01-overview.png` | 1280×1176 | 190978 | `904942af90ab52e2d5c1ef83db6d59673ad1e782ce57de0086a6c80e07a3b1ea` |
| `p8-chain-02-issue-sheet.png` | 1280×1303 | 213790 | `2f8f23b884e6a0971143679b2e27497f42447fa872b0bb06ca8f5a1e0c782525` |
| `p8-chain-03-collapsed.png` | 1280×1303 | 199944 | `aed155309522f9b23d66a3cd870868ca1278f9c6a5383905f5ce6b585e0aa9fb` |
| `p8-chain-04-abnormal-filter.png` | 1280×1176 | 166232 | `d88611d36d27777869b70088980ebed8d0054c03dee1214bc841c2f7348806ad` |
| `p8-chain-05-reconnecting.png` | 1280×1236 | 190775 | `218748ba22766851eac9c59984736baa2cfa1e9cdf78cb637d5cf3d625b3487e` |
| `p8-chain-06-truncated.png` | 1280×1136 | 128916 | `4aa28c0442806e080e1a099df10c23978b41796c73fbe7c3e3bb6141d7ee6345` |

## 2. axe + 键盘全流程（`chain-a11y.test.tsx`）

命令：

```powershell
cd frontend
pnpm vitest run src/features/execution/chain/chain-a11y.test.tsx
```

结果：`1 passed`（约 3.0 s，测试预算 60 s）。真实 `ExplorationTab` + React Flow 画布 + 隐藏 WAI-ARIA tree + Sheet + 工具栏渲染 40 需求 ×（1 用例 + 2 结果 + 1 问题）= 200 个节点：

- axe（`color-contrast` 因 jsdom 无布局引擎而禁用，与其他 a11y 门禁一致）在 200 节点态与 Sheet 打开态均无 serious/critical 违规；
- 键盘全流程：`↓` 需求→用例、`→` 进子节点、`↓` 结果→问题、`Enter` 打开 Sheet、`Esc` 关闭、`←` 折叠（可见 196、折叠 4）与 `→` 展开恢复 200、`Home`/`End` 首尾跳转、`Space` 再次打开 Sheet 并关闭；
- 只 mock 网络边界（`useExplorationChain`、`useCaseRun`），组件与布局/键盘/Sheet 全部是产品代码。

说明：jsdom 下 React Flow 的 MiniMap 在 200 节点时会向 stderr 打印 `Received NaN for the cx/cy/r/x/y attribute`（jsdom 无真实布局，React DOM 开发告警）；断言不受影响，退出码为 0。

## 3. 200 节点 / 400 边 P95（`slo-chain.ps1`）

脚本直接向运行中 API 所用数据库播种保留 id 的 200 节点图：50 需求 + 50 用例 + 50 结果 + 50 问题；每个用例关联 6 个需求（300 条 requirement_case 边），加 50 条 case_result、50 条 result_issue，共 400 边。脚本验证读模型返回恰好 200 节点/400 边后才计时。

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo-chain.ps1 `
  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://localhost:6868 `
  -DatabaseUrl $env:EZTEST_E2E_DATABASE_URL
```

结果（报告：`docs/acceptance/p8/slo-chain.md`；JSON：`.tmp/p8-slo-chain/metrics.json`）：

| 测量 | 阈值 | 实测 P95 | 状态 |
| --- | --- | --- | --- |
| `GET /api/versions/{id}/exploration-chain`（200 节点/400 边） | ≤ 800 ms | **85.1 ms**（P50 76.8 ms，20 采样） | PASS |
| 画布可交互（导航→200 节点 ARIA tree 就绪→`ArrowDown` 活跃节点移动） | ≤ 2500 ms | **1130 ms**（5 次测量，1 次预热不计） | PASS |

测量时 API/Web 由验收者以 e2e 数据库手动启动（`next dev`），测量后停止；屏幕外/首编译由预热排除。脚本在环境缺失时逐行写 SKIP（退出 0），真实浏览器测量失败则 FAIL（退出 1），不会把 SKIP 当 PASS；本次为真实测量 PASS。

## 4. 修改与边界

- 新增：`frontend/e2e/p8-exploration-chain.spec.ts`、`frontend/src/features/execution/chain/chain-a11y.test.tsx`、`scripts/slo-chain.ps1`、本文件与 6 张截图、`docs/acceptance/p8/slo-chain.md`。
- 修改：`scripts/e2e.ps1` 增加独立 P8 阶段（在 P7 与 `execution.spec.ts` 之前，避免执行器关闭后运行）。
- **未修改任何产品源码**。测试暴露的唯一 UX 观察：1280×720 下浮层筛选工具条会覆盖画布首行节点（初始 `fitView` 不感知 overlay），指针点击首行需先平移画布；本轮 E2E 通过列表视图打开问题 Sheet、通过 Sheet 内折叠按钮验证计数，未将其视为本任务的 P0 缺陷，建议视觉/交互任务评估是否调整 `fitView` padding 或工具条收起策略。

## 5. 复现

```powershell
cd frontend
pnpm vitest run src/features/execution/chain/chain-a11y.test.tsx
pnpm typecheck
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p8-exploration-chain
# SLO 需要运行中的 API/Web（可用验收脚本启动的 eztest_e2e 服务）：
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo-chain.ps1 `
  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://localhost:6868 `
  -DatabaseUrl $env:EZTEST_E2E_DATABASE_URL
```
