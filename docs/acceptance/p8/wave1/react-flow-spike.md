# P8 Task 5：React Flow 依赖 spike（W1 阻塞项）

- 验收时间：2026-10-29 11:47–11:56 +08:00
- 仓库/分支：`D:\Project_new\EzTest` / `dev`
- 基线 HEAD：`da3cbca0`（`test(p8): verify exploration limit defect on rebuilt head`）
- 目的：证明 `@xyflow/react` 12 在 EzTest 的 Next 16.4 / React 19.3 / Tailwind 4 / Vitest 5 栈里可安装、可懒加载、可在 jsdom 中测试、可随 Next 构建。
- 结论：**采用**。版本、懒加载方式与测试 polyfill 可固定下来，Task 10 据此实现正式 `ChainCanvas.tsx`。本文不包含纯 SVG 回退方案。

## 1. 实际版本

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| `@xyflow/react` | **12.12.0** | 直接依赖，`package.json` 写精确版本，无 `^` |
| `@xyflow/system` | 0.0.83 | `@xyflow/react` 的传递依赖 |
| `zustand` | 4.5.7 | 传递依赖 |
| `classcat` | 5.0.5 | 传递依赖 |
| Next.js | 16.4.3 | Turbopack 构建 |
| React / React DOM | 19.3.1 / 19.3.1 | |
| Tailwind CSS | 4.3.3 | 仅样式类名接入，spike 未新增 Tailwind 配置 |
| Vitest | 5.0.5 | |
| jsdom | 30.1.3 | |
| Node.js / pnpm | 24.18.1 / 11.28.5 | 本机安装与验证环境 |

运行依赖只新增 `@xyflow/react`；未新增 axe（P8 Task 22 再做）。

## 2. 安装与锁文件

命令（`frontend/` 下）：

```powershell
pnpm add @xyflow/react@12 --save-exact
```

结果：

- 退出码 `0`，耗时 32.1s；
- 实际解析并写入 **12.12.0**；
- `pnpm add @xyflow/react@12 --save-exact` 在本机 pnpm 11.28.5 下仍写成了 `^12.12.0`，随后执行 `pnpm add @xyflow/react@12.12.0 -E` 修正，最终 `package.json` 为 `"@xyflow/react": "12.12.0"`；
- pnpm 11 重新格式化 `pnpm-lock.yaml`（单引号）导致 `pnpm lint`（Biome 也检查 YAML）失败，已用 `pnpm exec biome check --write pnpm-lock.yaml` 恢复仓库既有的双引号格式，`pnpm install --frozen-lockfile` 与 `pnpm lint` 均通过；
- `pnpm install --frozen-lockfile`：退出码 `0`。

## 3. 懒加载方式

测试与真实路由都使用 `next/dynamic` 关闭 SSR，避免 React Flow 在服务端触碰 `window`：

```tsx
const ChainCanvasSpike = dynamic(
  () => import("./ChainCanvasSpike"),
  { ssr: false },
)
```

- `ChainCanvasSpike.tsx` 自身带 `"use client"`，并在模块内导入 `@xyflow/react/dist/style.css`。
- Next 16 App Router 中 `ssr: false` 只能出现在 Client Component 里；Task 10 接线时应由一个 `"use client"` 包装层执行上述 `dynamic(...)`，路由页（Server Component）只渲染包装层。
- 本 spike 为了不触碰 Wave 1 不允许改动的路由，没有把组件接入任何正式页面；构建验证用了一个临时探针路由（见 §5），验证后已删除，未提交。

## 4. Vitest / jsdom polyfill

新增 `frontend/vitest.setup.ts`，并在 `frontend/vitest.config.ts` 增加：

```ts
setupFiles: ["./vitest.setup.ts"],
```

| Polyfill | 原因 |
| --- | --- |
| `ResizeObserver` | jsdom 30 未实现；React Flow 用它测量容器与每个节点。实现里把同一 tick 的 `observe()` 合并成一次回调，200 节点只触发一次 store 更新 |
| `offsetWidth` / `offsetHeight` | jsdom 无布局、恒为 0；React Flow 的节点测量完全依赖这两个属性。polyfill 只对 `.react-flow`（800×600）、`.react-flow__node`（180×48）、`.react-flow__handle`（8×8）生效，其他元素仍返回 0，避免影响既有组件测试 |
| `DOMMatrixReadOnly` | React Flow 在 `updateNodeInternals` 中 `new window.DOMMatrixReadOnly(viewport.style.transform)` 读取 `m22` 缩放；jsdom 未实现。polyfill 支持 `matrix(...)` 与 `scale(...)` |
| `matchMedia` | jsdom 未实现；React Flow 用它读取 `prefers-color-scheme`。返回 `matches: false` 的最小实现 |

`getBoundingClientRect` 没有被覆盖：jsdom 的原生实现返回零矩形，React Flow 在本次用例里只用它计算 handle 相对偏移；节点“已测量/可见”的判定走 `offsetWidth/offsetHeight`，补上这两个属性后测试即可通过。Task 10 如果启用 `fitView` 或依赖 handle 精确坐标，需要再评估是否补 `getBoundingClientRect`。

## 5. 测试结果

目标用例：

```powershell
pnpm vitest run src/features/execution/chain/ChainCanvasSpike.test.tsx
```

- 退出码 `0`；`2 passed`；单次运行 1.21s。
- 用例 1：用 `dynamic(..., { ssr: false })` 渲染 2 节点链路，断言“需求：登录”“用例：登录成功”文本存在，且 React Flow 节点容器 `visibility` 为 `visible`；`console.error` 未被调用。
- 用例 2：渲染 200 节点 + 199 条边，测量 `render()` 到“节点 200”可见的 `performance.now()` 耗时，并 `console.log`：

```text
[react-flow-spike] 200 nodes visible in 209.9 ms
```

重复 3 次为 207.4ms / 205.0ms / 211.2ms；此前一次为 207.2ms。记录范围 **205–211ms**，远低于 5000ms 的防卡死阈值。该耗时不含首次 `next/dynamic` 模块加载（2 节点用例先执行，已完成加载），只覆盖 200 节点渲染与可见性等待。

全量前端测试：

```powershell
pnpm test
```

- 退出码 `0`；`96 passed (96)`，`851 passed (851)`，13.46s。新增 setup 未改变既有测试结果。

其他门禁：

```powershell
pnpm typecheck   # 退出码 0
pnpm lint        # 退出码 0（292 files）
```

## 6. 构建结果

最终提交树（无临时路由）执行 `pnpm build`：

- 退出码 `0`，总墙钟 **10.5s**；
- `Next.js 16.4.3 (Turbopack)`，`✓ Compiled successfully in 774ms`；
- TypeScript 检查 5.7s，静态页生成 12/12，539ms；
- 13 条正式路由全部输出，无新增路由；
- 同一棵树紧接着再构建一次（命中 TypeScript/Turbopack 增量缓存）总墙钟 5.4s，`Compiled successfully in 745ms`，TypeScript 1.7s。首次完整构建为 11.3s，可作为冷启动参考。

由于正式路由不会引用 spike 组件，单纯 `next build` 不会真正打包 `@xyflow/react`。为此额外做了一次**临时探针路由**验证（文件为未跟踪文件，验证后删除，未出现在提交中）：

- 新增 `src/app/(app)/p8-chain-spike-check/page.tsx`（`"use client"` + `dynamic(..., { ssr: false })`）；
- `pnpm build` 退出码 `0`，总墙钟 9.7s，`✓ Compiled successfully in 1438ms`，`Finished TypeScript in 5.6s`；
- 路由表出现 `○ /p8-chain-spike-check`（静态预渲染成功），说明 Turbopack 能解析 ESM 入口与 `@xyflow/react/dist/style.css`，且 `ssr: false` 边界在 Next 16 构建期成立；
- 删除探针后重新构建，得到上面的 10.5s 干净构建。

构建未改写 `frontend/next-env.d.ts`（`git diff` 为空），无需还原。

## 7. a11y 现状

- 画布根容器：`role="tree"` + `aria-label="探索链路画布（N 个节点）"`，作为 spike 的 a11y 冒烟入口。
- `<ReactFlow disableKeyboardA11y nodesDraggable={false} nodesConnectable={false} elementsSelectable={false}>`：关闭库内置键盘导航，避免与将来的树形键盘模型冲突。
- 自定义节点渲染纯文本标题（`data.label`），文本本身可被辅助技术读取。
- 已知缺口：根 `role="tree"` 下的节点目前没有 `treeitem` 语义，`aria-required-children` 等规则预计不通过；未安装 axe、未执行 axe 扫描。正式键盘/焦点模型与 axe 门禁留给 P8 Task 22。

## 8. 结论与 Task 10 注意事项

**结论：采用 `@xyflow/react` 12.12.0。**

Task 10 实现正式 `ChainCanvas.tsx` 时应保留：

1. `"use client"` 组件 + `next/dynamic(..., { ssr: false })` 的 Client Component 包装层；
2. `vitest.setup.ts` 中的 React Flow polyfill（尤其 `ResizeObserver` 与 `offsetWidth/offsetHeight`）；
3. 固定的画布尺寸与稳定在模块作用域的 `nodeTypes`，避免 React Flow 反复告警；
4. a11y 需要从“根 role=tree + 文本可读”扩展为完整的 tree/treeitem、焦点与键盘模型，并接入 axe 门禁（Task 22）。

若 Task 10 启用 `fitView`、MiniMap 交互或 handle 精确坐标，需要复核 §4 中 `getBoundingClientRect` 的缺口；必要时再补 polyfill，而不是降级依赖或改回手写 SVG。

## 9. 变更文件

- `frontend/package.json`
- `frontend/pnpm-lock.yaml`
- `frontend/vitest.config.ts`
- `frontend/vitest.setup.ts`（新增）
- `frontend/src/features/execution/chain/ChainCanvasSpike.tsx`（新增）
- `frontend/src/features/execution/chain/ChainCanvasSpike.test.tsx`（新增）
- `docs/acceptance/p8/wave1/react-flow-spike.md`（本文件）
