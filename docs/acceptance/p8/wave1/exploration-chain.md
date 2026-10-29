# P8 Task 13：探索链路 E2E / 可访问性 / 200 节点性能验收

- 验收时间：2026-10-29 16:44–16:48 +08:00（fix round 1 重跑）
- 仓库/分支：`D:\Project_new\EzTest` / `dev`
- 基线 HEAD（开始时）：`9730248`。工作区同时存在并行 Task 28 的未提交 backend 改动；本任务未修改、未提交这些文件。
- 验收范围：真实后端 + 真实 Chrome 的探索链路 E2E、200 节点 axe/键盘门禁、200 节点/400 边 P95。
- 结论：**全部通过**。E2E 1 passed（1280×720 下用真实画布 pointer 点击问题节点，并用 `document.elementFromPoint` 证明未被工具栏遮挡）；axe 在明确禁用的 `color-contrast` 之外无 serious/critical；链路 API P95 **92.2 ms**（阈值 800 ms）；画布可交互 P95 **1109 ms**（阈值 2500 ms）。

## 1. 探索链路 E2E

命令（仓库根目录，自动重置并启动 `eztest_e2e` API/Web/Fake LLM/Executor/Fixture）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p8-exploration-chain
```

结果：`1 passed (20.2s)`，`==> E2E PASSED`，退出码 `0`。真实数据由 `seedP7ExecutionVersion` 播种：一个失败基线批次触发 P7 失败分析/问题整理，生成带证据的 AI 问题；补全批次再执行通过的 AI 补全用例，因此正常/异常结果同时存在。

修复后的指针路径：加载 `?tab=exploration` 并切到「全部批次」后，不切换列表视图、不预先平移画布，直接点击画布上的 `chain-node-issue-*`。点击前在 1280×720 视口内断言：问题节点中心在视口内、`document.elementFromPoint` 命中的元素属于该节点、且不属于 `chain-toolbar`；随后 Playwright 的真实 pointer 点击打开 Sheet。

| 验收项 | 实际断言 | 结果 |
| --- | --- | --- |
| 打开 `?tab=exploration` | `exploration-tab` 可见 | PASS |
| 无错误条 | `exploration-error` / `exploration-degraded` / `chain-issues-degraded` 计数均为 0 | PASS |
| 四类节点 | 从真实 API 快照取 requirement/case/result/issue 各一，节点与卡片均可见 | PASS |
| 可见计数 | UI「可见 N 个节点」= 读模型 `nodes.length` | PASS |
| 工具栏不遮首行 | 工具栏位于画布视口上方独立行（`chain-workspace-toolbar` / `chain-canvas-viewport`）；问题节点中心 `elementFromPoint` 不在 `chain-toolbar` 内 | PASS |
| 问题节点 Sheet | **真实画布 pointer 点击**问题节点 → `chain-sheet` 打开；证据区、`加载证据（N）`、加载后的证据按钮、「查看问题」深链均可见 | PASS |
| 问题深链 | 点击「查看问题」→ `issues` Tab 打开且 `issue-detail` 为同一问题 id | PASS |
| 折叠/展开计数 | 折叠后可见数下降且文案含「折叠 M 个」；展开后恢复到折叠前数值 | PASS |
| 仅异常 | 勾选后 URL 带 `abnormal=1`，可见数等于异常读模型节点数；通过的补全结果节点为 0 个 | PASS |
| 断线模拟 | 拦截 `/api/versions/*/stream*` 后 `chain-stream-status` 显示「重连中」，同时画布节点仍渲染 | PASS |
| >100 需求 fixture | psql 播种 101 条需求后显示 `已显示前 100 条（共 101 条）` 与「需求层已截断」；截图前等待 `chain-node-*` 已渲染；断言后删除 101 条需求与 throwaway version 并复验为 `0|0` | PASS |
| console/秘密 | 只过滤 favicon 404 与 React DevTools 提示；API/页面资源的其他 404 与 pageerror 仍会使测试失败；管理员与 fixture 密码均未出现在页面文本 | PASS |

### 截图

| 文件 | 尺寸 | 字节 | SHA-256 |
| --- | --- | --- | --- |
| `p8-chain-01-overview.png` | 1280×1176 | 205012 | `f46f4798d27ce15d8644254d5bea1a47b240f4cfc8f821eed4307b09d2897709` |
| `p8-chain-02-issue-sheet.png` | 1280×1176 | 280843 | `61966b74b709f7eaa7ed9cef6d39b87d05acc7f7e989d258de2fc839b366d53f` |
| `p8-chain-03-collapsed.png` | 1280×1303 | 201064 | `489d7d6fc2b99d50ff2d303eea492098a2b0b330f5cdfd9152b874edab58c1b3` |
| `p8-chain-04-abnormal-filter.png` | 1280×1176 | 183466 | `afbbf9ed2982a2d40c848380a5684215db202697d0d35894db8c06e152f51af4` |
| `p8-chain-05-reconnecting.png` | 1280×1236 | 200168 | `de233b7188ab13b19b9431bc3c3254310d986af13657d8ff0590a230cdc9b037` |
| `p8-chain-06-truncated.png` | 1280×1136 | 142266 | `79006559729fb35f74d6db273b609f8aea7e7d30312fe56b1e8d4f104f31d608` |

## 2. axe + 键盘全流程（`chain-a11y.test.tsx`）

命令：

```powershell
cd frontend
pnpm vitest run src/features/execution/chain/chain-a11y.test.tsx src/features/execution/chain src/features/execution/components/ExplorationTab.test.tsx
```

结果：17 files / 171 tests passed。真实 `ExplorationTab` + React Flow 画布 + 隐藏 WAI-ARIA tree + Sheet + 工具栏渲染 40 需求 ×（1 用例 + 2 结果 + 1 问题）= 200 个节点：

- axe（仅禁用 `color-contrast`）在 200 节点态与 Sheet 打开态均无 serious/critical 违规；
- **`color-contrast` 因 jsdom 无布局/绘制引擎而禁用；本门禁不声称 contrast 通过。contrast 由 Task 22 与人工/视觉评审覆盖，不能把本结果表述为「无排除项的 axe clean」。**
- 键盘全流程：`↓` 需求→用例、`→` 进子节点、`↓` 结果→问题、`Enter` 打开 Sheet、`Esc` 关闭、`←` 折叠（可见 196、折叠 4）与 `→` 展开恢复 200、`Home`/`End` 首尾跳转、`Space` 再次打开 Sheet 并关闭；
- 只 mock 网络边界（`useExplorationChain`、`useCaseRun`），组件与布局/键盘/Sheet 全部是产品代码；
- 新增结构断言：工具栏与 React Flow 视口分离（`chain-canvas-viewport` 位于工具栏之后，canvas 在其内部），防止工具栏回到覆盖画布首行的旧布局。

说明：jsdom 下 React Flow 的 MiniMap 在 200 节点时会向 stderr 打印 `Received NaN for the cx/cy/r/x/y attribute`（jsdom 无真实布局，React DOM 开发告警）；断言不受影响，vitest 退出码为 0。

## 3. 200 节点 / 400 边 P95（`slo-chain.ps1`）

脚本直接向运行中 API 所用数据库播种保留 id 的 200 节点图：50 需求 + 50 用例 + 50 结果 + 50 问题；每个用例关联 6 个需求（300 条 requirement_case 边），加 50 条 case_result、50 条 result_issue，共 400 边。脚本验证读模型返回恰好 200 节点/400 边后才计时。

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo-chain.ps1 `
  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://localhost:6868 `
  -DatabaseUrl $env:EZTEST_E2E_DATABASE_URL
```

结果（报告：`docs/acceptance/p8/slo-chain.md`；JSON：`docs/acceptance/p8/slo-chain-metrics.json`，同时镜像到 `.tmp/p8-slo-chain/metrics.json`）：

| 测量 | 阈值 | 实测 P95 | 状态 |
| --- | --- | --- | --- |
| `GET /api/versions/{id}/exploration-chain`（200 节点/400 边） | ≤ 800 ms | **92.2 ms**（P50 82.4 ms，20 采样） | PASS |
| 画布可交互（导航→200 节点 ARIA tree 就绪→`ArrowDown` 活跃节点移动） | ≤ 2500 ms | **1109 ms**（5 次测量，1 次预热不计） | PASS |

测量时 API/Web 由验收者以 `eztest_e2e` 数据库手动启动（API 使用 e2e 构建产物，Web 使用 `next dev`），测量后停止；屏幕外/首编译由预热排除。脚本在环境缺失时逐行写 SKIP（退出 0），真实浏览器测量失败则 FAIL（退出 1），不会把 SKIP 当 PASS；本次为真实测量 PASS。原始 metrics JSON 不再只留在 gitignored 的 `.tmp`，已复制到 `docs/acceptance/p8/slo-chain-metrics.json` 供审计。

## 4. 修改与边界

- 产品修复：`frontend/src/features/execution/components/ExplorationTab.tsx` 把筛选工具栏放进画布上方独立行（`chain-workspace-toolbar`），React Flow 视口改为其下方的 `chain-canvas-viewport`（`flex-1`），使 `fitView` 计算的可用区域不再包含工具栏；`frontend/src/features/execution/chain/ChainCanvas.tsx` 不再给画布自身钉 `min-h-[420px]`（高度由视口行拥有），避免画布溢出到工具栏下方。
- 测试：`ChainCanvas.test.tsx`、`chain-a11y.test.tsx` 增加/更新“画布填满视口、工具栏与画布分离”的结构断言；`frontend/e2e/p8-exploration-chain.spec.ts` 改为真实画布 pointer 点击问题节点并做命中校验，收窄 404 console 过滤，截断 fixture 增加清理与截图前节点等待。
- 脚本：`scripts/slo-chain.ps1` 额外写出 `docs/acceptance/p8/slo-chain-metrics.json`。
- 未修改 backend/server/config/executor/settings，也未触碰工作区中并行任务的未提交改动；`frontend/next-env.d.ts` 在 `next dev` 后变脏，提交前已还原。

## 5. 复现

```powershell
cd frontend
pnpm vitest run src/features/execution/chain/chain-a11y.test.tsx src/features/execution/chain src/features/execution/components/ExplorationTab.test.tsx
pnpm typecheck
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p8-exploration-chain
# SLO 需要运行中的 API/Web（可用验收脚本启动的 eztest_e2e 服务）：
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo-chain.ps1 `
  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://localhost:6868 `
  -DatabaseUrl $env:EZTEST_E2E_DATABASE_URL
```
