<h1 align="center">EzTest</h1>

<p align="center"><strong>AI 驱动、半自主的 Web 与 HTTP API 端到端测试系统。</strong></p>

<p align="center">
  简体中文 | <a href="README.md">English</a>
</p>

<p align="center">
  <img alt="Version: 0.2" src="https://img.shields.io/badge/version-0.2-blue">
  <img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  <img alt="Go 1.24" src="https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white">
  <img alt="Next.js 16" src="https://img.shields.io/badge/Next.js-16-black">
  <img alt="PostgreSQL 17" src="https://img.shields.io/badge/PostgreSQL-17-4169E1">
  <img alt="Node 22" src="https://img.shields.io/badge/Node-22-339933">
</p>

---

## 目录

- [概述](#概述)
- [工作方式](#工作方式)
- [功能](#功能)
- [快速开始](#快速开始)
- [运维与部署](#运维与部署)
- [许可证](#许可证)

---

## 概述

EzTest 接收一个发布版本及其需求，产出经过确认、可执行、带证据的端到端覆盖。人只做真正需要判断的部分：

> 在对话中澄清需求 → 打开设计对话 → 确认产出的用例。

其余工作——设计场景与用例、把确认转成执行波次、驱动真实浏览器与 HTTP API、分类失败、
创建与合并问题、渲染报告、推动版本收尾——都由 Agent 团队在确定性平台内核之上完成。

控制面模型有五个核心特征：

- **任务板是唯一的执行队列。** 每个 Agent 动作（包括一次聊天回合）都是带团队、类型、优先级、
  去重键、租约和 fencing epoch 的 `work_item`。Reconciler 对比期望与实际状态并补建缺失任务，
  事件只用于提前唤醒。
- **四个团队 + Version Steward。** 澄清、设计、执行、交付团队通过持久的版本×团队频道和产物
  交接协作；没有中心编排 Agent，团队之间不互相调用。
- **版本状态是投影，不是闸门。** `initial → running → completed`，出现新目标时自动重开；
  已完成版本仍然可用。
- **原生多 SUT。** 一个版本可以绑定多个系统，每个系统有自己的环境、页面、API、只读 PostgreSQL
  目标、账号/角色和本地 Git 仓库。用例可以逐步切换 SUT，证据和断言按 `version_sut_id` 归属。
- **证据绑定结论。** 结果是 `passed` / `failed` / `blocked` / `inconclusive`；失败分为应用、
  环境、会话、模型或用例问题。截图、控制台、网络、API 响应和视觉 diff 都按内容寻址且不可变。
- **最小启动配置。** API 进程只读取 4 个启动环境变量；其余运行时配置都在数据库设置中心。

## 工作方式

一次正常旅程包含六步：

1. **管理员配置。** 注册系统、环境、页面、API、只读目标库、账号/浏览器会话、本地 Git 仓库和 LLM。
2. **版本与需求。** 创建版本、绑定 SUT、录入需求，由澄清团队把对话转成带版本的草稿需求。
3. **设计。** 打开设计对话；设计团队扫描待设计范围，产出 场景 → 用例 → 步骤 → 断言（含需求来源、
   SUT 目标和代码锚点），持续生成草稿直到人工确认。
4. **执行。** 确认产生执行意图；planner 组成波次（每版本一个活跃波）。用例执行器驱动真实 Chrome
   和 HTTP，可选只读数据库断言和视觉断言。
5. **交付。** 失败变成带证据的问题候选；波次/版本报告是追加式修订（HTML 为准，PDF 由独立 worker
   渲染）；探索链路解释 需求 → 用例 → 结果 → 问题 的血缘。
6. **收尾与继续。** 所有工作和收尾谓词满足后，Version Steward 提议完成。新需求、需求重开或新确认
   用例会让版本回到 `running`，进入下一个增量波次。

UI 由 **8 个全局壳层项**——版本、问题、报告、知识、资源、质量、Agent 中心、设置——和 **8 个版本
Tab**——概览、需求与澄清、用例设计、执行、探索链路、问题、报告、代码与变更——组成。全局搜索
（`Ctrl/Cmd+K`）和待办抽屉位于顶栏；知识是全局的，不归属单个版本。

## 功能

| 领域 | 能力 |
|---|---|
| 需求与澄清 | 多需求版本、澄清对话、不可变需求版本、单条/批量确认、关系、附件、AI 回退提议和知识注入 |
| 用例设计 | 以设计对话为主 UI，场景/用例/步骤/断言模型，版本化用例，按用例/场景/需求确认，current+confirmed 执行语义，补充与缺口 draft |
| 控制面与 Agent 团队 | 带租约/fencing 的共享任务板、reconciler、版本×团队频道、19 种任务、14 个 Agent profile、上下文包和预算化运行 |
| 多 SUT 执行 | 一个版本多个 SUT、步骤级 SUT 路由、按角色的浏览器上下文/账号、环境指纹、带每 SUT 配额的多系统调度 |
| 执行与证据 | 自动波次、暂停/恢复/检查点恢复、四态结果与失败分类、内容寻址的截图/控制台/网络/API/视觉证据、真实执行器注册/心跳/容量/drain/按执行器派发 |
| 代码智能与视觉断言 | 只读本地 Git 索引、树/文件/diff/搜索/提交、带代码锚点的影响分析与事实复核；视觉选择器/区域/阈值断言、已批准基线与 diff 证据 |
| 交付 | 问题确认、合并、重开、跨波次复发与去重；追加式报告修订与 `as_of`/stale/regenerate；HTML + PDF 报告；导出 |
| 探索链路 | 需求→用例→波次→结果→证据→问题的交互图，支持过滤、深链接、问题侧栏、回放和截断元数据 |
| 全局工作区 | 版本切换、全局搜索、待办中心、移动端导航、深/浅色主题、跨版本只读问题与报告中心 |
| 知识 | global/system 作用域知识，draft → 审核 → approved/revoked 生命周期，从版本晋升，PostgreSQL 全文注入、引用预算与使用记录 |
| 质量与 Agent 中心 | 确定性与真实 LLM 评测轨、设计/影响/分类/补充指标、基线回归规则、提示词 revision/预览/回退与运行观察 |
| 运维 | 内嵌追加式迁移、readiness/leader 选举、带在飞计数的 drain、实例级限流（429 + `Retry-After`）、事件/审计保留清理、GC dry-run/apply、备份/恢复脚本 |

## 快速开始

### 前置条件

| 要求 | 说明 |
|---|---|
| PostgreSQL 17 | 本地或远程；开发默认 `localhost:5433`。 |
| Go 1.24 | `backend/go.mod` 声明 Go `1.24.6`。 |
| Node.js 22+ 与 pnpm | Web、执行器和 PDF renderer 使用。 |
| Chromium 内核浏览器 | Google Chrome、Microsoft Edge 或 Playwright Chromium；真实用例执行需要。 |

### 1. 只配置 4 个启动变量

复制 `.env.example` 为 `.env`（已 gitignore），设置数据库 URL；三个端口有默认值：

| 变量 | 用途 | 默认 |
|---|---|---|
| `EZTEST_DATABASE_URL` | PostgreSQL 连接 URL | 必填 |
| `EZTEST_API_PORT` | API 监听端口 | `13333` |
| `EZTEST_WEB_PORT` | Web 端口；CORS 来源由它推导 | `6868` |
| `EZTEST_EXECUTOR_PORT` | 执行器端口；默认执行器 URL 由它推导 | `13336` |

没有第 5 个启动变量。路径、LLM 凭据、执行器 URL、限额、保留期、限流等运行时配置都在数据库设置
中心，通过 UI 的**设置**管理。

### 2. 启动开发栈

```powershell
# 先复制并编辑 .env。
Copy-Item .env.example .env

# 仅首次使用随附便携 PostgreSQL 工具链时：
pwsh .tools/setup-toolchain.ps1
pwsh scripts/db.ps1 init

# API + Web + 执行器，健康检查通过后打印 URL。
pwsh scripts/dev-all.ps1
```

`scripts/dev-all.ps1` 会应用内嵌迁移、种子管理员、启动 API/Web/执行器并等待真实 readiness。常用变体：

```powershell
pwsh scripts/dev-all.ps1 -Status   # 探测运行中的栈
pwsh scripts/dev-all.ps1 -Stop     # 停止 API/Web/执行器（PostgreSQL 保留）
pwsh scripts/dev-renderer.ps1      # 可选：PDF renderer 监听 127.0.0.1:13337
```

打开 <http://localhost:6868>，用首次启动时种子的管理员登录。如果使用自己的 PostgreSQL 17 服务，
把 `EZTEST_DATABASE_URL` 指向它并跳过 `db.ps1 init`。便携工具链会创建 `eztest` / `eztest_test`
数据库：把 URL 指向 `/eztest`，或创建 URL 中指定的数据库（`.env.example` 默认 `eztest_v02`）。

| 组件 | 默认 URL |
|---|---|
| Web | <http://localhost:6868> |
| API liveness / readiness | <http://localhost:13333/healthz> · <http://localhost:13333/api/health/ready> |
| 执行器健康 | <http://127.0.0.1:13336/healthz> |
| PDF renderer 健康 | <http://127.0.0.1:13337/healthz> |

### 3. 测试

```powershell
pwsh scripts/test.ps1              # Go 单测 + 集成，前端 lint/typecheck/vitest
cd executor; pnpm test             # 执行器 node:test
cd renderer; node --test           # renderer node:test
```

## 运维与部署

- **本地开发：** API `:13333`、Web `:6868`、执行器 `:13336`、可选 renderer `:13337`、
  PostgreSQL `:5433`。
- **Docker Compose：** [deploy/README.md](deploy/README.md) 描述六服务栈（双 API 副本、Web、
  PostgreSQL、执行器、renderer）。
- **多副本：** API 副本通过 PostgreSQL 租约和 leader advisory lock 协调；readiness 暴露
  `inflight_requests`/`inflight_work`，实例可以 drain，所有副本必须共享一个内容目录。限流按实例计：
  总允许速率 = 副本数 × `security.rate_limit_per_minute`（默认 `0` = 关闭）。
- **保留：** `retention.event_days`（默认 90）和 `security.audit_retention_days` 由 leader 日循环
  执行；GC 默认 dry-run，apply 需要显式确认。
- **备份/恢复：** 使用 `scripts/backup.ps1` / `scripts/restore.ps1`；同时备份 PostgreSQL 与内容目录。

## 许可证

基于 [Apache License 2.0](LICENSE) 许可。
