# EzTest 0.2 P8 验收运行手册（Runbook）

> 性质：**P8 验收证据/运行入口**，不是设计文档；不新增设计目录文件。
> 日期：2026-11-02。分支：`feat/v0.2-p8`。设计依据：
> [architecture-design.md](../../architecture-design.md) §10–§16、
> [high-level-design.md](../../high-level-design.md) §8–§12、
> [P8 阶段设计规格](../../../superpowers/specs/2026-11-02-eztest-v02-p8-hardening-acceptance-design.md) §11。
> 部署入口：[deploy/README.md](../../../../deploy/README.md)。

本手册给出三件事的运行入口：**本地开发**、**P8 验收**、**多副本运行**。P8 验收结果文档
（`acceptance / resilience / multi / e2e / capability-signoff / static / known-limitations`）在最终
验收运行后记录到本目录；本文件只负责“怎么跑、证据在哪、已知边界是什么”。

## 1. 环境与前置

### 1.1 主机依赖

| 依赖 | 要求 |
|---|---|
| 操作系统 | Windows 10/11 + PowerShell 5.1 或 PowerShell 7（验收脚本以 Windows 为先） |
| PostgreSQL | 17，默认本机 `:5433`；验收库 `eztest_v02_p8` |
| Go | 1.24（`backend/go.mod` 为 `1.24.6`；仓库便携工具链在 `.tools/go`） |
| Node.js / pnpm | Node 22+、pnpm 11；Web、executor、renderer 各自安装依赖 |
| 浏览器 | 真实 Google Chrome（Playwright `channel=chrome`）或等价的 Chromium |
| LLM | OpenAI 兼容端点（验收使用 DeepSeek `deepseek-flash`），配置在 DB 设置中心而不是 `.env` |
| Docker | **不需要，也未运行**；Compose 只做静态校验 |

### 1.2 `.env`：恰好 4 个启动变量

复制 `.env.example` 为 `.env`（gitignored），只写这 4 项：

| 变量 | 用途 | 默认 |
|---|---|---|
| `EZTEST_DATABASE_URL` | PostgreSQL 连接 URL | 必填 |
| `EZTEST_API_PORT` | API 监听端口 | `13333` |
| `EZTEST_WEB_PORT` | Web 端口；CORS origin 由此派生 | `6868` |
| `EZTEST_EXECUTOR_PORT` | 执行器端口；默认执行器 URL 由此派生 | `13336` |

没有第 5 个启动变量。执行器 URL/token、LLM 凭据、路径、并发、保留期、限流等都通过设置中心
（`system_setting`）配置。Compose 另需 `POSTGRES_*` / `EZTEST_EXECUTOR_TOKEN` 等插值变量，见
[deploy/README.md](../../../../deploy/README.md)；它们不是应用启动变量。

### 1.3 端口与拓扑

| 组件 | 端口 | 说明 |
|---|---|---|
| API A | `13333` | 单副本验收、日常开发 |
| API B | `13344` | 多副本/滚动升级 |
| Web | `6868` | Next.js |
| Executor A | `13345` | 真实验收主执行器 |
| Executor B | `13346` | 多执行器/离线重派 |
| PDF renderer | `13337` | 独立进程，Bearer token |
| PostgreSQL | `5433` | 验收库与克隆源 |
| Fixture SUT | `14080` / `14081` | `scripts/fixture-target.cjs`，`FIXTURE_VARIANT=orders` / `payments` |

验收内容根（多副本共享）：`.tmp/p8-acceptance/data`（evidence / reports / exports / objects /
attachments）。单机目录等价于“共享文件系统”；所有副本必须指向同一目录。

### 1.4 数据库与真实 LLM

```sql
-- 在 postgres 库执行；P7 验收脚本会从 eztest_v02_p6 重建 eztest_v02_p7
DROP DATABASE IF EXISTS eztest_v02_p8 WITH (FORCE);
CREATE DATABASE eztest_v02_p8 TEMPLATE eztest_v02_p7;
```

> 克隆源 `eztest_v02_p6` 必须先存在（P6 验收库）。`p7-acceptance.ps1` 只负责用
> `-TemplateDatabase eztest_v02_p6` 重建 `eztest_v02_p7`；缺少 P6 库时先按 P6 验收流程准备。

- 验收前在设置中心确认至少一个可用 LLM 配置并执行 `config test`；真实 LLM 路径不使用 fake 数据。
- 验收前确认 executor `/healthz` 返回 `browserVersion`（Chrome 可启动）、renderer `/healthz` 为
  200、API `/api/health/ready` 为 200。
- P8 脚本会自行启动 fixture/executor/renderer/API；手工运行前先释放上述端口。

## 2. 本地开发运行

```powershell
# 1) 首次：便携工具链 + 初始化数据库（或改用自己的 PostgreSQL 17，跳过这两步）
pwsh .tools/setup-toolchain.ps1
pwsh scripts/db.ps1 init

# 2) 一键启动 API + Web + Executor（健康检查通过后才打印 URL）
pwsh scripts/dev-all.ps1

# 3) 可选：PDF 渲染器（PDF 报告需要）
pwsh scripts/dev-renderer.ps1
```

管理命令：

```powershell
pwsh scripts/dev-all.ps1 -Status   # 探测运行状态
pwsh scripts/dev-all.ps1 -Stop     # 停止 API/Web/executor；PostgreSQL 保留
pwsh scripts/db.ps1 stop           # 同时停止便携 PostgreSQL
```

> 内置便携工具链的 `db.ps1 init` 创建 `eztest` / `eztest_test`；`.env.example` 默认库名是
> `eztest_v02`。首次运行前把 URL 的库名改为 `eztest`，或自行创建 URL 中的库。

健康检查：

| 端点 | 含义 |
|---|---|
| `GET http://127.0.0.1:13333/healthz` | API 进程存活 |
| `GET http://127.0.0.1:13333/api/health` | 数据库、版本串、可用 LLM 配置、限流/GC 设置 |
| `GET http://127.0.0.1:13333/api/health/ready` | readiness；含 `inflight_requests`/`inflight_work` |
| `GET http://127.0.0.1:13336/healthz` | executor 健康与 `browserVersion` |
| `GET http://127.0.0.1:13337/healthz` | renderer 健康 |

回归测试（不产生验收证据，但必须是绿的）：

```powershell
pwsh scripts/test.ps1
cd executor; pnpm test
cd renderer; node --test
node scripts/p8-contract-check.cjs
```

## 3. P8 验收运行

### 3.1 脚本顺序

P8 验收驱动位于 `scripts/p8-*.ps1`。推荐顺序如下；聚合入口 `scripts/p8-all.ps1` 按同一顺序执行并汇总
（先跑 P9 回归，再克隆 `eztest_v02_p8`，再跑 P8 场景）。

| 顺序 | 命令 | 场景 | 证据 |
|---|---|---|---|
| 0 | `pwsh scripts/test.ps1`；`node scripts/p8-contract-check.cjs` | 回归 + 契约门禁 | 控制台 exit 0 |
| 1 | `pwsh scripts/p7-acceptance.ps1` | S7：P9 五项回归（13 场景、156 checks） | `.tmp/p7-acceptance/`、[p7 acceptance](../p7/acceptance.md) |
| 2 | 克隆 `eztest_v02_p7` → `eztest_v02_p8`（见 §1.4） | 环境准备 | clone SQL/时间戳 |
| 3 | `pwsh scripts/p8-acceptance.ps1` | S0–S6：全旅程（管理员配置、双 SUT、真实 LLM 澄清/设计、真实 Chrome 执行、交付/收尾/重开） | `.tmp/p8-acceptance/` |
| 4 | `pwsh scripts/p8-resilience.ps1` | S9–S10：长上下文/长执行、硬化定向（设置热更、多执行器、PDF、版本、雪花、限流、保留清理） | `.tmp/p8-resilience/` |
| 5 | `pwsh scripts/p8-multi.ps1` | S8：双副本、滚动升级、leader 接管、kill 恢复、SSE、真实执行器离线重派 | `.tmp/p8-multi/` |
| 6 | `pwsh scripts/p8-capability.ps1` | S11：37 行能力矩阵 smoke/签字 | `docs/version-V0.2/acceptance/p8/capability-signoff.md` |
| 7 | `pwsh scripts/p8-e2e.ps1` | S12：浏览器全旅程/全局页（真实 API/DB/Chrome，不触发真实 LLM，复用 S0–S6 数据） | `.tmp/p8-e2e/`、`acceptance/p8/screenshots/` |
| 8 | `pwsh scripts/p8-static-check.ps1` | S13：4 env、文档/链接、契约计数、Compose、0.1 门禁引用、secret | 控制台 exit 0 |

要点：

- `p8-acceptance.ps1` 默认重置克隆库；复跑用 `-SkipDatabaseReset`，环境已就绪时传
  `-DatabaseName eztest_v02_p8 -ApiPort 13333`。
- 每个场景使用独立版本/前缀，避免互相污染；脚本负责进程与端口清理。
- 首轮允许 FAIL：把每个 FAIL 记入 `.tmp/p8-acceptance/bug-notes.txt` 并按 §11.6 分级；修复后必须复跑，
  不得靠改产品代码/直接写 DB 绕过。
- 真实 LLM 输出不稳定时最多重试 2 次；仍失败按产品/提示词缺陷处理，**不得伪造数据**。
- 收尾场景等待 Version Steward 自动收尾（上限 10 分钟），不以直接改 DB/状态代理。

### 3.2 证据与发布

- 运行期证据：`.tmp/p8-acceptance/`（`scenarios.txt`、`acceptance.log`、API/executor/web 日志、
  JSON/HTML/PDF、截图）、`.tmp/p8-resilience/`、`.tmp/p8-multi/`、`.tmp/p8-e2e/`。
- 提交版证据：`docs/version-V0.2/acceptance/p8/`：
  `acceptance.md`、`resilience.md`、`multi.md`、`e2e.md`、`capability-signoff.md`、`static.md`、
  `known-limitations.md`、`scenarios.txt`、`screenshots/`。
- 每条证据必须能回答：命令、环境（库/端口/版本）、数据、时间戳、退出码、结论。
- **P8 最终结果是验收运行后写入上述文件的；本 runbook 不预写 PASS。**

## 4. 多副本运行

多副本演练由 `scripts/p8-multi.ps1` 驱动（进程生命周期与端口由脚本管理；参数用
`Get-Help scripts/p8-multi.ps1` 查看）。手工运行时遵守以下约束：

1. API A 与 API B 使用**同一个** `EZTEST_DATABASE_URL` 和**同一个**内容根；`EZTEST_API_PORT` 分别为
   `13333` / `13344`。实例 `node_id` 由注册流程分配（首选 `runtime.snowflake_node_id`，被占用时取最小
   空闲 0–1023），不需要为两个进程分别设置。
2. 两个 API 都注册为 `api_instance`；leader 由 PostgreSQL advisory lock 选出。reconciler/scheduler/
   GC/保留清理只在 leader 上运行。
3. 滚动升级顺序：B 启动并 ready → `POST /api/admin/instances/{id}/drain` 排空 A → A readiness 变 503
   →等待 `active_requests`/`active_work` 归零 → 停止 A → B 接管 leader。
4. 故障演练：kill -9 一个副本后，存活副本接管；`running` 租约只在过期后回收，不因新实例启动批量中断。
5. 执行器：`execution.executor_url` ∪ `execution.executor_urls` 为端点集合；P8 起按容量从全部可派发
   端点选择（`owner_executor_id` / `case_run_sut_context.executor_id` 记录真实执行器）。执行器心跳
   超时→`offline`，其 case_run 置 `interrupted(executor_lost)`、`dispatch_epoch++` 并按 intent 重派。
6. 跨实例 SSE：事件经 PostgreSQL `LISTEN/NOTIFY` fan-out；`Last-Event-ID` 回放与 `stream.reset`
   语义不变。

手工启动两个 API 时不要在同一个 `.env` 下直接跑两次 `scripts/dev-api.ps1`（它会按 `.env` 覆盖端口）。
使用多副本驱动脚本，或在启动前为每个进程单独设置环境变量（数据库 URL、API 端口、内容根必须一致）。

## 5. 已知限制（运行前必须知道）

1. **P8 最终验收结果尚未记录**：本 runbook 只定义入口；以 `acceptance/p8/` 最终文档为准。
2. **Docker 未实跑**：本机无 Docker；Compose/Dockerfile 从未构建或启动，只做静态校验。真实 Docker
   主机上首次运行需复核镜像构建、网络、共享卷、renderer URL/token。
3. **SLO 未测量、安全审计未执行**：按用户 2026-11-02 裁定跳过（P8-D-03 / P8-D-04）；不设 SLO 阈值、
   不产出安全审计报告，也不使用 0.1 `security-audit.ps1`。
4. **fake-LLM 回归/性能基准不做**（P8-D-01）；验收全程真实 LLM + 真实 Chrome，不拿离线替身当证据。
5. **数据迁移**：验收库是 P7 克隆，不做 0.1 原地升级；0.1 数据不迁移。
6. **限流默认关闭**：`security.rate_limit_per_minute=0`；验收临时设 30 验证 429 + `Retry-After`，结束
   后恢复 0。限流是**实例级**，总速率 = 副本数 × 限额。
7. **保留清理**：事件默认 90 天、审计默认 90 天；清理只在 leader 日循环执行；事件清理后旧游标回放走
   `stream.reset` 语义。
8. **PDF 大小上限 10 MiB**：与内容账本一致；验收只做 10 MiB ± 1 的单元边界，不生成 10 MiB 级真实
   PDF。
9. **多副本边界**：要求共享文件系统；不做自动扩缩容/跨区域 HA/Redis 共享限流桶；单机目录等价于
   NFS 的验收口径。
10. **浏览器/端口**：真实验收依赖本机 Chrome 与固定端口；端口冲突时先清理旧进程。没有可用 executor
    时批次 fail-closed（`503 executor_unavailable`），不会伪装通过。
11. **E2E 范围**：S12 浏览器 E2E 不触发真实 LLM（真实旅程由 S3–S6 覆盖），页面数据来自 S0–S6 验收库。
12. **0.1 脚本仅归档**：`acceptance.ps1`、`slo.ps1`、`security-audit.ps1`、`build-release.ps1` 等
    不是 0.2 验收驱动，详见 [docs/version-V0.1/README.md](../../../version-V0.1/README.md)。
