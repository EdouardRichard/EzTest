# EzTest executor（协议 v2 · 通用工作站）

EzTest 执行服务：一个**通用 Agent 工作站**（executor 协议 v2.0），不是固定动作的浏览器客户端。
Go API 持有 Bearer token 并经 HTTP 驱动本服务；执行器**只执行与采集，不理解测试语义、不写业务表**
（AR-001）。浏览器对执行 agent 不直接暴露——agent 经能力域工具（后端注册表）调用本服务的通用通道。

- **协议契约**：[`docs/contracts/executor-protocol-v2.md`](../docs/contracts/executor-protocol-v2.md)（端点/形状/错误码/限额/租约 fencing）。
- **能力清单**：[`docs/contracts/capability-list.md`](../docs/contracts/capability-list.md)（按能力域生成、与注册表同源、三清单）。
- **设计依据**：设计 v1.3.1 §4.1–4.6（P6–P11）、GD-01/21/22；C spec SC-C1/C2/C3。

> **v2-only（v0.2.4-C，R-C2）**：v1 协议面（`/v1/contexts` 族 9 端点、15 个语义动作含 `visual.compare`/`close`、
> `/v1/explore` 族 3 路由）已**一步删除**，无遗留路由、无双轨。删除清单与通知期见
> [`docs/v0.2.4/20-v0.2.4-release-notes.md`](../docs/v0.2.4/20-v0.2.4-release-notes.md) C 章节。

## 能力域

工具面按**能力域**组织（域名 = 工具名首段命名空间；注册表是单一权威）。本服务提供通用通道，
后端把通道封装为域工具（便捷封装，**非上限**；通用 exec/shell 是逃生阀，宪法二.6）：

| 能力域 | 本服务通道 | 说明 |
|---|---|---|
| 通用执行域 | `exec` / `shell`（PTY）/ `files` | argv[] 默认、shell=true 显式、stdin、后台进程+PID、进程组取消；PTY 交互（ssh/psql/mysql/REPL）；workspace 文件上传/下载/列 |
| 浏览器域 | `exec playwright-cli` + 被动 CDP 录制 | 官方 `@playwright/cli` 全子命令透传（`browser.cli` 工具是便捷封装）；流量纯录制 |
| HTTP/API 域 | 经 `exec`（curl 等） | 后端 `http.request` 便捷封装（P8 解限：any-host + spill） |
| 数据域 | 经 `shell`（psql 等） | 后端 `db.query` 通用 SQL 读写（P8 解限） |
| 技能 | 后端技能库（`skill.read`） | SKILL.md 渐进披露 + skill_usage 账本；浏览器/侦察工作法的知识载体（运行态可编辑，不硬编码） |

## 本地运行

```powershell
# terminal 1: executor on http://127.0.0.1:13336
$env:EZTEST_EXECUTOR_TOKEN = "dev"   # or put it in the repo .env
.\scripts\dev-executor.ps1

# terminal 2: API, which should see the same token and URL
.\scripts\dev-api.ps1
```

API 用 `EZTEST_EXECUTOR_URL`（默认 `http://127.0.0.1:13336`）与 `EZTEST_EXECUTOR_TOKEN`。
`scripts/dev-api.ps1` 只打印 executor 健康提示，不启动 executor。

**浏览器运行时（P10/Q3=B，ARTEX 模型）**：浏览器域跑**官方 `@playwright/cli`**，全局安装、**`@latest` 浮动**
（镜像重建即升级；每个 CLI 版本钉死自己的引擎，可能 alpha）。本地开发需自行安装：

```powershell
npm install -g @playwright/cli@latest
playwright-cli install-browser chromium --with-deps
```

CLI 未安装时工作站**仍就绪**（exec/shell/files 不需要浏览器）；只有浏览器命令返回 typed
`browser_cli_unavailable`（503），绝不静默回退到别的运行时。

## 配置

| 变量 | 默认 | 含义 |
|---|---|---|
| `EZTEST_EXECUTOR_ADDR` | `127.0.0.1:13336` | 监听地址；容器覆盖为 `0.0.0.0:13336` |
| `EZTEST_EXECUTOR_TOKEN` | 必填 | 每个 `/v1` 端点的 Bearer；也是 push Bearer |
| `EZTEST_EXECUTOR_MAX_SESSIONS` | `8` | 会话容量（满 → 429 + Retry-After） |
| `EZTEST_EXECUTOR_LEASE_TTL_MS` | `300000` | 会话租约 TTL（对齐后端 executor_context_ttl_seconds） |
| `EZTEST_EXECUTOR_RECLAIM_INTERVAL_MS` | `10000` | 孤儿回收 tick（0 禁用） |
| `EZTEST_EXECUTOR_COMMAND_TIMEOUT_MS` | `900000` | 命令默认超时（15min，运维参数） |
| `EZTEST_EXECUTOR_STDIO_INLINE_LIMIT_BYTES` | `65536` | stdio inline 界（超则 spill 到 workspace） |
| `EZTEST_EXECUTOR_JSON_BODY_LIMIT_BYTES` | `4194304` | files JSON 体上限（4MiB） |
| `EZTEST_EXECUTOR_ARTIFACT_LIMIT_BYTES` | `209715200` | artifact/上传/下载内容上限（200MiB） |
| `EZTEST_EXECUTOR_ARTIFACT_BUFFER_LIMIT` | `512` | 每会话内存 artifact 保留（0=不限） |
| `EZTEST_EXECUTOR_SHELL_BUFFER_LIMIT_BYTES` | `1048576` | 每 shell 内存输出界（0=不限） |
| `EZTEST_EXECUTOR_WORKSPACE_RETENTION` | `keep` | 回收时 workspace `keep`/`delete` |
| `EZTEST_EXECUTOR_STATE_DIR` | `<tmp>/eztest-executor-sessions` | 崩溃恢复注册表目录 |
| `EZTEST_EXECUTOR_SHUTDOWN_GRACE_MS` | `30000` | 优雅停机 drain 宽限 |
| `EZTEST_PLAYWRIGHT_CLI_JS` | unset | 显式 playwright-cli JS 入口（覆盖自动解析） |
| `EZTEST_API_URL` | unset | 设则启用 push 注册/心跳；空禁用（pull-only） |
| `EZTEST_EXECUTOR_HEARTBEAT_SECONDS` | `10` | push 心跳间隔（1–60） |
| `EZTEST_EXECUTOR_NAME` | `hostname:port` | push register 上报名 |

完整限额与错误码见协议契约 §6/§7。**v1 的 `EZTEST_EXECUTOR_MAX_CONTEXTS`/`EZTEST_EXECUTOR_ALLOW_DATA_PAGES`/
`EZTEST_EXEC_ALLOWED_HOSTS` 已删除/忽略。**

## HTTP 协议（摘要）

除 `/healthz` 外全部要求 `Authorization: Bearer <token>`。完整形状见协议契约。

| Method | Path | 用途 |
|---|---|---|
| GET | `/healthz` | 工作站就绪（`status:"ok"\|"degraded"`，**不探浏览器**） |
| GET/POST | `/v1/drain` | 查询/开关 drain |
| GET/POST | `/v1/sessions` | 会话列表/创建 |
| GET/DELETE | `/v1/sessions/{id}` | 会话详情/删除 |
| POST | `/v1/sessions/{id}/exec` | 执行命令 |
| POST | `/v1/sessions/{id}/cancel` | 取消运行中命令 |
| POST | `/v1/sessions/{id}/lease` | 续租/epoch 接管 |
| GET/POST | `/v1/sessions/{id}/shell` | PTY shell 列表/创建 |
| GET | `/v1/sessions/{id}/shell/{shellId}` | shell 详情 |
| GET | `/v1/sessions/{id}/shell/{shellId}/read` | 读输出（字节游标） |
| POST | `/v1/sessions/{id}/shell/{shellId}/write\|close\|wait` | 写/关/等待 |
| GET/POST | `/v1/sessions/{id}/files` | 列/下载（GET）/上传（POST） |
| GET | `/v1/sessions/{id}/artifacts` | 拉取录制账本（游标） |
| GET | `/v1/sessions/{id}/trace` | trace 文件引用 |
| GET | `/v1/sessions/{id}/network` | 浏览器流量读取（纯录制） |

`/healthz` 报告**会话服务**：`{status,version,sessions,maxSessions,draining,reclaimedSessions,browserCli?}`。
缺 Chromium **不** degrade（exec/shell/files 无需浏览器）；drain 是唯一 degraded 态（503）。
`browserCli:{cli_version,engine_version}` 是可复现指纹（CLI 已安装时）。**v1 的 `browserVersion`/`contexts`/
`maxContexts` 字段已删除**——就绪只判 `status:"ok"`（200）。

## 录制层（证据）

每条命令/文件变更/trace/浏览器输出自动结算为不可变 artifact（内容寻址 sha256，携带 base64 供后端落证据）。
单一 kind 注册表：`command/file/trace/screenshot/snapshot/console/network/download`；**未声明 kind 失败封闭**
（宪法四.5）。env **全量明文**录制（GD-01）；大 stdio spill 到 workspace 并显式 `*_truncated`/`*_ref`；
丢弃计数 + 显式事件，绝不静默丢（宪法七.6）。浏览器流量经**被动 CDP 录制**（纯录制无拦截；注入失败显式降级，
仅浏览器录制）。

## 信任模型（GD-01，无安全层）

单管理员内部可信部署，executor 宿主机**原生执行**——**无沙箱、无资源上限、无审批门、无出网白名单、无凭据脱敏**。
凭据明文 by design（Q05=B），可进 prompt/证据/日志；仅 API 出参掩码。Bearer token 是 API↔executor 的**功能性管道**
（保留），不是权限工程。本文中的 size/timeout/容量界限是**运维性资源管理**（可配、默认开放），**不是能力限制**。
v1 的 host 白名单/scheme 拦截/六套守卫/四条 secret 脱敏已**全部移除**（R-C3）。文件通道的 workspace 路径限制是
**功能性 workspace 完整性**（会话文件落在其 workspace），不是宿主隔离——exec/shell 仍原生跑。

## 测试

```powershell
cd executor
pnpm install
pnpm test
```

`node --test`（`--test-concurrency=1`），本地 `http.createServer` fixture 页；不依赖 `scripts/fixture-target.cjs`
或 Go 栈。套件起真实 headless Chromium（浏览器域测试），需可用的 Playwright 浏览器缓存或本机 Chrome/Edge channel。

## Docker

`Dockerfile` 基于 `node:22-bookworm`；全局浮动安装 `@playwright/cli@latest` 并 `playwright-cli install-browser
chromium --with-deps`（CLI 自身钉死的引擎）。容器健康检查（`src/healthcheck.mjs`）只判 `GET /healthz` 200
（不依赖 curl、不探浏览器）。Compose 把 executor 作为 internal-only 服务，注入与 API 共享的 token。
**Docker/Compose 构建与运行尚未在本机验证**（宪法七.2 如实披露）；依赖容器路径前请在 Docker 主机上构建运行。