---
name: playwright-cli
description: 官方 playwright-cli 浏览器自动化用法（browser.cli 透传）：快照 ref 定位、交互、多标签、iframe、上传下载、等待、网络与控制台读取、登录态保存加载。
---

# playwright-cli 浏览器操作技能

本技能是 `browser.cli` 工具的知识载体。`browser.cli` 把官方 `@playwright/cli` 的子命令**原样透传**到当前 case_run 的 executor 会话内执行——它不是固定动作集，完整命令面都可用；本技能只教你怎么用对，不限制你能用什么。CLI 未覆盖的能力用 `exec.run`（通用逃生阀）直接跑 `playwright-cli` 或其他程序。

## 会话与调用约定（EzTest 特有）

- 用 `browser.cli` 时**不要传 `-s`/`--session`**：CLI 命名会话由 executor 绑定到当前执行会话，传了会被拒绝。选择会话用工具参数 `sut_id` / `account_role`。
- 每条命令及其 snapshot/截图/trace/console 输出**自动落为不可变证据**，不需要手工把输出复制进证据。
- 全局 flag：`--raw` 只输出结果值（适合 `exec.run` 里接 jq/diff 管道）；`--json` 输出 JSON 信封。
- 命令面随 `@playwright/cli@latest` 浮动；拿不准的子命令先用 `exec.run` 跑 `playwright-cli --help <command>` 查实际参数，不要凭记忆猜。

## 核心工作流：快照 → ref → 操作

1. `open <url>` 打开页面（或 `goto <url>` 导航）。
2. `snapshot` 取 ARIA 快照；每个可交互元素带 ref（如 `e15`）。快照跨 iframe 渲染，iframe 内元素同样有 ref（形如 `f1e2`）。
3. 用 ref 操作：`click e15`、`fill e15 "文本"`、`select e15 "值"`、`hover e15`、`check`/`uncheck`、`dblclick e15`（双击）。target 也可以是唯一 CSS 选择器或 Playwright locator（如 `getByRole('button', { name: '提交' })`）。
4. 每次操作后自动返回新快照；ref 会随页面变化重新分配，**操作前用最新快照的 ref**，不要复用旧 ref。
5. `find <文本>`（可 `--regex`）在快照内搜索元素；`snapshot --depth N` 限深、`--boxes` 带坐标。

## 常用命令分组

- 导航/键盘：`goto`、`go-back`、`go-forward`、`reload`、`press <key>`、`type <text> [--submit]`。
- 鼠标：`click`/`dblclick [button] [--modifiers=...]`、`drag <起点> <终点>`、`mousemove`/`mousedown`/`mouseup`/`mousewheel`。
- 多标签/弹窗：`tab-list`、`tab-new [url]`、`tab-select <index>`、`tab-close [index]`。`window.open`/`target=_blank` 弹窗就是新 tab，用 tab-* 切换。
- 文件上传：`upload <绝对路径...>`（可多文件；先让文件输入框就绪，必要时先 click 触发文件选择器）；页外拖放用 `drop <target> --path=<绝对路径>`。
- 对话框（alert/confirm/prompt）：`dialog-accept [文本]` / `dialog-dismiss`。
- 截图/导出：`screenshot [target] [--full-page] [--filename]`、`pdf`。
- 视口/仿真：`resize <宽> <高>`；`open --device/--mobile`；`set-color-scheme`、`set-media` 等。移动端断言先切到对应视口。
- 登录态：`state-save [文件名]` 保存 cookies+storage、`state-load <文件名>` 注入；`cookie-list/get/set/delete`、`localstorage-*`、`sessionstorage-*` 细粒度读写。
- 网络读取：`requests [--all] [--filter=<regex>]` 列已记录流量，`request <index>`、`request-headers/body <index>`、`response-headers/body <index>` 读明细。
- 网络 mock：`route <pattern> [--status --body --content-type --header]`、`route-list`、`unroute [pattern]`；`network-state-set online|offline` 模拟断网。
- 控制台：`console [min-level] [--all] [--clear]`。
- 录制：`tracing-start` / `tracing-stop`（trace 自动落证据）。

## 没有专名命令的能力（用 run-code 逃生阀）

- **等待**：没有 `wait` 子命令，`goto` 也没有 wait_until flag（默认等 load）。显式等待用：
  - `run-code "async page => { await page.waitForLoadState('networkidle'); }"`（load | domcontentloaded | networkidle）
  - `run-code "async page => { await page.locator('.loading').waitFor({ state: 'hidden' }); }"`
  - `run-code "async page => { await page.waitForFunction(() => window.appReady === true); }"`
  - 用例要求的时间窗口（如“120 秒后弹框消失”）用 `execution.wait`，不要空转重复读取。
- **iframe 复杂操作**：快照 ref 直接可点 iframe 内元素；需要帧级 API 时 `run-code "async page => { const frame = page.locator('iframe#x').contentFrame(); await frame.locator('button').click(); }"`。
- **下载**：没有 `download` 子命令：`run-code "async page => { const d = page.waitForEvent('download'); await page.getByRole('link',{name:'下载'}).click(); const dl = await d; await dl.saveAs('./f.pdf'); return dl.suggestedFilename(); }"`。
- 页内取值/断言辅助：`eval "<函数>" [target]`。

## EzTest 执行纪律

- 每完成一步调用 `execution.step_record`，每条断言调用 `execution.assertion_record`；expected 以任务上下文为准，服务端覆写。
- 断言失败/阻塞时证据已自动落库（命令/快照/截图/console），必要时补 `execution.evidence` 文本说明，然后 `execution.case_finish`。
- 登录用 `account.credentials` 取凭据后经 `fill` 输入；凭据明文 by design，可进证据与日志。验证码/2FA 用 `human.request(kind=login_takeover)`。
- 连续失败 2 次或找不到可用入口时及时 `case_finish(verdict=blocked)` 说明缺口，不要空转重试耗尽预算。