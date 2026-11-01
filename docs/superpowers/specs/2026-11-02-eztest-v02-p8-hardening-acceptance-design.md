# EzTest 0.2 P8（硬化与验收）阶段设计规格

> 状态：待用户审阅；设计已于 2026-11-02 获用户口头批准（P8-D-01…P8-D-12 裁定见 §3）。
> 阶段文件：`docs/version-V0.2/phases/phase-08-hardening-acceptance.md`；上一 handoff：`docs/version-V0.2/phases/phase-07-handoff.md`。
> 基线：分支 `feat/v0.2-p8`（从 `feat/v0.2` @`e2cf110` 拉出）；工作树保留用户未提交的 phase-08 范围修改（`fake LLM 回归`/`性能/`/`安全审计` 已删）。
> 依据：`docs/version-V0.2/architecture-design.md`、`high-level-design.md`、`research/{decision-log,open-decisions,capability-matrix}.md`、`implementation-phase-plan.md`、P0–P7 handoff、`contracts/` 全部文件、`ui/mock.html`、`scripts/` 与代码现状审计。
> 不修改：`docs/version-V0.2` 下的设计文档（architecture/high-level/research/ui/README/phases 非本阶段文件）。

---

## 1. 目标、范围与成功标准

### 1.1 目标

把 0.2 从“功能齐”推到“可交付”：在真实 LLM、真实 Chrome、双 SUT、双副本条件下跑通全量旅程，复核 P9 五项与长上下文/长执行/故障恢复，修复 P7 handoff 点名的硬化缺口，冻结 P8 契约，签字 0.1 P0–P8 能力矩阵，并把根目录产品文档升级到 0.2。**不新增大功能**，只做修复/硬化/验收。

### 1.2 In（本阶段必须交付）

1. **全量旅程验收**：管理员配置资源（双 SUT/环境/页面/API/库/账号/代码仓）→ 创建版本并绑定 → 真实 LLM 澄清 → 真实 LLM 设计 → 用户确认 → 真实 Chrome 执行（含跨系统用例）→ 问题/报告/链路 → 收尾 → 重开。
2. **P9 五项回归**：复用 `scripts/p7-acceptance.ps1`（S0–S12）在 P8 环境重跑并引用证据；P8 额外补真实性缺口（真实 Chrome 离线重派、真实 drain 计数、收尾谓词全链路）。
3. **长上下文/长执行**：滚动摘要、钉住事实、compaction、tool 结果落盘、检查点恢复；`context_overflow` 不作为终态失败。
4. **多副本/滚动升级/故障恢复**：双 API 副本、共享存储、leader 接管、drain 等待 in-flight 归零、kill -9、跨实例 SSE、执行器离线 `interrupted(executor_lost)` + 重派；不重复执行、不批量中断。
5. **P7 遗留硬化 11 项**：见 §4.2（H1–H11）。
6. **契约 P8 冻结**：openapi `0.2.0-p8` / `x-frozen: P8`；新增设置 2 项；迁移 #41/#42；契约文本一致性修复；`scripts/p8-contract-check.cjs`。
7. **能力矩阵签字**：37 行（A1–I2），逐行给 0.2 入口、P8 证据、既往证据与结论。
8. **文档同步**：根目录 `README.md`、`README.zh-CN.md`、`DEVELOPMENT_STATUS.md` 升级为 0.2；0.1 设计/手册加归档横幅；新增 0.2 运行/部署手册入口；验收证据落 `docs/version-V0.2/acceptance/p8/`。
9. **CI 去 0.1 化**：`.github/workflows/ci.yml` 静态门禁切换为 0.2 `p8-static-check`；0.1 脚本保留但不再被 CI 引用。
10. **缺陷修复与回归**：验收发现的产品缺陷在 P8 修复并回归；无未关闭 P0/P1。

### 1.3 Out（本阶段不做）

- **SLO 测量**：按用户 2026-11-02 裁定“不用测，跳过这项”。phase-08 的 SLO 交付项记为**用户批准的偏离**，handoff 记录；不产生 SLO 报告、不设阈值。
- **安全审计**：按用户裁定“不做安全审计”。不运行 0.1 `security-audit.ps1`、不产出安全审计报告；仅在验收中遵守 secret 不入库/不入提交的既有约束。
- **fake-LLM 确定性回归 / 性能基准**：按用户对 phase-08 未提交修改的确认，不做。
- **Docker 实跑**：本机无 Docker；Compose/Dockerfile 只做静态校验，明确标注“未实跑”。
- 0.3 新能力、生产级安全加固、密钥托管、TLS/多租户、跨版本问题合并、远程浏览器/VNC、Excel 导出、0.1 数据导出脚本。
- 修改 `docs/version-V0.2` 设计文档；恢复 0.1 控制面假设；复制 ARTEX 源码。

### 1.4 成功标准（映射 phase-08 五条）

| # | phase-08 验收 | P8 证据 |
|---|---|---|
| 1 | 全量旅程在真实 LLM/真实浏览器/多 SUT 下跑通 | `p8-acceptance.ps1` S0–S6 PASS；`acceptance/p8/acceptance.md` |
| 2 | 长上下文/长执行无 `context_overflow` 终态失败；恢复/压缩可查 | `p8-resilience.ps1` S9 PASS；`context.compacted`/`conversation_summary`/`context_packet` 证据 |
| 3 | 多副本/滚动升级/故障恢复不重复执行、不批量中断 | `p8-multi.ps1` S8 PASS；计数断言 + `interrupted(executor_lost)` 证据 |
| 4 | P9/P0–P8 能力矩阵全绿；无 P0/P1 缺陷 | P7 验收 13/13 + 37 行签字 + 缺陷门禁（§11.6） |
| 5 | 用户验收通过 | Lead 代验收；用户审阅 `phase-08-handoff.md` 与验收证据后确认 |

---

## 2. 现状核对（分支 `feat/v0.2-p8` @`e2cf110`，逐项核验）

| 能力/项 | 现状 | 证据 |
|---|---|---|
| 后端上下文/表/设置/迁移/契约 | 20 上下文、85 业务表、83 设置、40 迁移、openapi `0.2.0-p7`（162 paths/200 ops/264 schemas）、118 事件、19 任务、14 profiles | `backend/internal/settings/application/catalog.go`、`backend/internal/db/migrations/order.txt`、`contracts/openapi.yaml:4,:154` |
| 运行时版本串 | **漂移**：`0.2.0-p5` vs 契约 `0.2.0-p7` | `backend/cmd/eztest/main.go:70`、`contracts/openapi.yaml:4` |
| API/实例/执行器健康 | `/healthz`、`/api/health`、`/api/health/ready`、实例注册/心跳/drain/leader 已实现 | `backend/internal/server/health.go`、`backend/internal/orchestration/application/service.go` |
| 设置热更新 | 同进程缓存更新；**跨实例失效未实现**（仅启动 `Reload`） | `settings/application/service.go:71-86`、`cmd/eztest/main.go:553`、P7 handoff §4.2 |
| in-flight drain 计数 | **无**；readiness 仅 DB/存储/draining；`InstanceView` 无计数 | `orchestration/application/ports.go:107-119`、`orchestration/interfaces/httpapi/handler.go:80-101` |
| 多执行器派发 | 注册/心跳/多端点 poll/容量选择已就绪；**派发仍限主端点** | `cmd/eztest/p7_wiring.go:413-440`、`execution/application/scheduler.go:200-206`、`executor-protocol.md §12.3` |
| 知识注入 FTS | repository 有 `websearch_to_tsquery`/`ts_rank`；**调用方 query 为空** | `agent/application/knowledge.go:114-124`、`agent/infrastructure/knowledgeadapter/adapter.go:71-89` |
| 事件/审计清理 | **无 worker**；`security.audit_retention_days=90` 已定义未消费 | `settings/application/catalog.go:162`、`eventing_0001_events.sql:16`、`audit_0001_audit_log.sql:14` |
| 限流 | **无**；无 429/Retry-After 契约 | 全库 grep；`contracts/openapi.yaml` 无 429 |
| PDF 上限 | **不一致**：render client 20 MiB vs content ledger 10 MiB | `delivery/infrastructure/renderclient/client.go:21-22`、`storage/application/service.go:29` |
| 雪花/实例 ID | 雪花同毫秒序列已逻辑进位；实例 node 分配靠唯一约束重试，`ErrInstanceIDTaken` 直接失败 | `platform/id/snowflake.go:42-65`、`orchestration/application/service.go:126-174` |
| E2E 固定 seed | p6/p7 spec 默认硬编码旧克隆的 ID | `frontend/e2e/p6.spec.ts:11`、`frontend/e2e/p7.spec.ts:25-37` |
| CI 静态门禁 | 仍跑 0.1 `check-ci.ps1` + `acceptance.ps1 -StaticOnly` + `check-docs.ps1`，与 0.2 `.env.example` 冲突 | `.github/workflows/ci.yml` static-acceptance job |
| 0.1 资产 | `acceptance.ps1`(329KB)/`slo.ps1`/`security-audit.ps1`/`build-release.ps1`/`acceptance-p8-llm.ps1` 等均为 0.1 树，不能按名字复用 | P0/P6/P7 handoff；脚本头注 |
| 测试基线 | 316 个 Go `*_test.go`/102 包；105 个 vitest 文件；e2e 8 spec/15 test；executor 111、renderer 23 | 审计统计；P7 handoff §3 |
| P7 验收资产 | `p7-acceptance.ps1` S0–S12（156 checks/170s）、`p7-e2e.ps1` 5 场景/21 图、`p7-multi.ps1`、`p7-contract-check.cjs` | `docs/version-V0.2/acceptance/p7/`、`.tmp/p7-acceptance/final2/` |
| 文档现状 | 根 README 仍是 v0.1-preview；`docs/*.md` 为 0.1 P8 手册；无 `acceptance/p8/`、无 `phase-08-handoff.md` | 根目录与 `docs/` 审计 |
| 本机环境 | 真实 DeepSeek key（`.env`）、Chrome 可用；PostgreSQL 便携版未启动；**无 Docker** | 环境探测 |

**已知契约文本矛盾（P8 冻结时修复，非语义变更）**：db-schema §5 表数量（45 vs 实际 85）；settings §2 计数（44/49 vs 83）；settings §4g“全部热加载”与启动读取项矛盾；openapi 文件级 `x-frozen: P2`/`P3 draft` 残留；task-types §4 与 §8 重复保留 `report.render_pdf`；executor-protocol §10 与 §12 push 重复保留；P7 handoff §2 与 §4.5 版本串不一致。

---

## 3. 用户裁定（2026-11-02，已批准）

| ID | 问题 | 裁定 | 影响 |
|---|---|---|---|
| P8-D-01 | phase-08 未提交修改是否生效 | **生效**：不做 fake-LLM 回归、不做性能基准、不做独立安全审计；保留修改，不覆盖 | 范围收窄；handoff 记录 |
| P8-D-02 | 分支 | 新建 `feat/v0.2-p8`（从 `feat/v0.2`），完成合回 `feat/v0.2` | 阶段隔离 |
| P8-D-03 | SLO | **跳过，不测量** | phase-08 SLO 交付项记为偏离 |
| P8-D-04 | 安全审计 | **不做** | 不产出安全审计报告；不运行 0.1 脚本 |
| P8-D-05 | P7 遗留硬化 | **全部**（含限流、事件/审计保留清理） | 见 §4.2 |
| P8-D-06 | 验收环境/规模 | 克隆 `eztest_v02_p7`→`eztest_v02_p8`；demo 双 SUT；真实 LLM/Chrome；长上下文调低 window+≥30 消息；长执行 ≥12 用例/2 波；双 API 副本 | 见 §11.2 |
| P8-D-07 | 能力矩阵签字 | P8 全旅程 + P7 验收 + 37 项 smoke/抽查 + 既往证据；不重跑 P1–P6 全部脚本 | 见 §11.5 |
| P8-D-08 | 打包/部署 | 静态校验 + 文档，明确记录未实跑 Docker | 见 §11.3 S13 |
| P8-D-09 | 文档同步 | 根目录 README/README.zh-CN/DEVELOPMENT_STATUS 升级到 0.2；0.1 文档归档标注；不重写 0.1 设计内容 | 见 §12 |
| P8-D-10 | 契约/版本 | 无破坏性变更；仅追加设置；openapi+runtime→`0.2.0-p8`，冻结 P8 | 见 §6 |
| P8-D-11 | 多执行器派发与冻结语义 | 批准实现 per-executor 派发，并把 executor-protocol §12.3 的“P8 backlog”改为 P8 已实现（语义转正，不改线协议） | 见 §4.2 H4 |
| P8-D-12 | 缺陷门禁 | Lead 代验收：交付时无未关闭 P0/P1；P2 记录处置；P3 backlog | 见 §11.6 |
| P8-D-13 | 限流默认值 | `security.rate_limit_per_minute` 默认 `0`（关闭）；验收临时设 30 验证 429；文档写“总速率=副本数×限额” | 见 §7 |
| P8-D-14 | 事件保留 | 新增 `retention.event_days=90`；审计复用已有 `security.audit_retention_days=90`；清理在 leader 日循环执行 | 见 §7 |

---

## 4. 总体方案

### 4.1 交付拓扑

```text
真实用户/浏览器 (Next.js web :6868)
        │
API A :13333 ──┐        共享 PG (便携版 :5433) + 共享 .data
API B :13344 ──┴── leader (PG advisory lock)
        │
        ├── executor A :13345 (Node/Playwright, 真实 Chrome)
        ├── executor B :13346 (Node/Playwright, 真实 Chrome)
        └── renderer :13337 (Node/Playwright PDF)
目标系统：scripts/fixture-target.cjs ×2（orders/payments 两端口，双 SUT）
真实 LLM：DB 设置中心（DeepSeek deepseek-flash）
```

- 单副本验收用 API A；多副本/滚动用 A+B；双执行器用于跨系统与离线重派。
- 共享 `.data` 使用同一目录（单机等价 NFS），readiness 存储探针验证可写。
- 不新增外部依赖；不引入 Docker/队列/对象存储。

### 4.2 硬化设计（H1–H11）

#### H1 跨实例设置缓存失效

- `settings.Service` 已有 `RWMutex` 与 `Reload`；缺的是跨实例触发。
- 扩展 `eventingpg.Listener`：新增 `WithListenerObserver(func(ctx, event))`（或等价回调），在事件已加载、广播本地 Hub 后回调；不改变 NOTIFY 回放/去重语义。
- 组装根注册 observer：`event.Type == "settings.updated"` → `settingsSvc.Reload(ctx)`；错误只记日志，不影响事件投递。
- 去抖：同一实例 100ms 内的多次刷新合并（可选实现；测试允许最终一致 ≤2s）。
- 测试：settings 单测（observer 触发 Reload）；eventing 单测（observer 被调用一次）；集成测试（PG NOTIFY + 两 Service，B 在 A 更新后看到新值）。
- 契约：`settings.md` 热加载说明更新；无 API 变化。

#### H2 in-flight drain 计数

- 迁移 #41 给 `api_instance` 增加 `active_requests INTEGER NOT NULL DEFAULT 0`、`active_work INTEGER NOT NULL DEFAULT 0`（均 `CHECK >= 0`）。
- 新增 `orchestration/application.LoadTracker` 端口：`InflightRequests() int`；`InflightWork() int`。缺省 nil 时计 0。
- 组装根实现：`backend/internal/platform/load.Counter` 两个进程内原子计数器；HTTP 中间件对非 health/SSE 请求 `Inc/defer Dec`；agent `Runner` 在 run 执行入口 `Inc`、退出 `Dec`（case 执行在 agent run 内，天然覆盖）。不跨上下文读表、不新增 DB 查询。
- `Service.runHeartbeat` 每轮把两个计数写入 `api_instance`（扩展 `Repository.Heartbeat` 签名）；`InstanceView` 与 `GET /api/health/ready` 增加 `active_requests`/`active_work`（readiness 字段名 `inflight_requests`/`inflight_work`）。
- 语义：`draining` 时 readiness 仍 503（不接新流量）；滚动升级等待计数归零后再停进程。不改变 drain 的“停止新认领”语义。
- 测试：迁移集成；heartbeat 计数写入/读取；readiness 字段；server 中间件计数（含 SSE 长连接不计/或计入并说明）；滚动升级等待逻辑（脚本断言）。
- 契约：db-schema、openapi 加字段；无迁移外的新表。

#### H3 知识注入 FTS query

- `agent/application.KnowledgeReader.RetrieveForVersion` 增加 `query string` 参数；`Runner.prepare` 用 `versionPack.Title` + `versionPack.Requirements`（去重、截断 ≤512 字符）构造 query。
- `knowledgeadapter.Adapter` 把 query 传入 `knowledgeapp.InjectInput.Query`；repository 现有 `websearch_to_tsquery('simple', $2)`/`ts_rank` 生效；query 为空时保持现有 `id DESC` 语义。
- 注入预算、scope、source_stale、usage 记录语义不变。
- 测试：adapter 透传 query；service 注入使用 query；集成测试命中标题相关条目且不命中无关条目；空 query 回归。
- 契约：`agent-profiles-tools.md` §10.3 补 query 派生规则；无 API 变化。

#### H4 多执行器派发（per-executor routing）

- 迁移 #42 给 `case_run_sut_context` 增加 `executor_id BIGINT`（可空，逻辑引用 `executor_instance.id`，不建 FK）。
- `execution.BrowserExecutor` 端口扩展为按执行器路由：
  - `CreateContext` 使用 `BrowserContextSpec.ExecutorID`（由 `CaseRunner` 从 `caseRun.OwnerExecutorID` 填入）。
  - `Action`/`Cancel`/`Close` 增加 `executorID int64` 参数；调用方从 `case_run_sut_context.executor_id`（或当前 case_run owner）解析。
  - `ListContexts` 改为按执行器（或由恢复流程逐执行器调用）；保持外部 HTTP 协议不变。
- 基础设施新增 `executor.ClientResolver`：按 endpoint 缓存 `HTTPClient`（`sync.Map`/互斥 map）；`BrowserAdapter` 接受 resolver + `ExecutorLookup`（按 id 取 endpoint）。
- 组装根用完整 `Registry` 替换 `primaryExecutorRegistry`；`Registry.Select` 的多端点容量排序生效；`owner_executor_id` 记录真实执行器；`case_run_sut_context.executor_id` 落库。
- 单端点兼容：`execution.executor_urls` 为空时行为与 P3 完全一致；`owner_executor_id` 仍写主端点行。
- 测试：registry 选择次级端点；resolver 按 id 路由；scheduler 两个 run 落在两个 executor（fake）；集成测试两个 fake executor 各自收到 context/action；single-endpoint 回归。
- 契约：`executor-protocol.md §12.3` 更新为 P8 已实现（per-executor 路由、容量排序生效），删除“P8 backlog”限制；线协议仍 v1.2，无 action 变化。

#### H5 PDF 上限对齐

- `renderclient.MaxPDFBytes` 从 `20 << 20` 改为 `10 << 20`，与 `storage.MaxUploadBytes` 一致；错误信息/测试同步。
- 测试：10 MiB 边界（等于通过、超 1 字节拒绝）；renderer 仍返回 2xx 但超限 → `ErrRenderRejected`。
- 契约：openapi PDF 说明与 P7 handoff 偏差表更新；无设置变化。

#### H6 API 版本串

- `backend/cmd/eztest/main.go` 与 `internal/server` 测试统一 `0.2.0-p8`；`GET /api/health` 返回该值。
- `contracts/openapi.yaml` `info.version: 0.2.0-p8`、根 `x-frozen: P8`；`contracts/README.md` 增加 P8 冻结段。
- 测试：p5/server wiring 版本断言更新；`p8-contract-check.cjs` 断言版本串。

#### H7 E2E 动态 seed

- `frontend/e2e/p6.spec.ts`、`p7.spec.ts`：删除硬编码默认 ID；`beforeAll` 登录后通过 API 发现实体（版本/报告/知识/问题），仍保留 `EZTEST_E2E_*` 环境变量覆盖。
- 新增 `p8.spec.ts` 只使用本场景创建的实体，不依赖固定 seed；必要时通过 API 创建并清理。
- 测试：在重组后的 P8 克隆库上 p6/p7/p8 spec 全绿，无需手工 env 覆盖。

#### H8 雪花/实例 ID 并发

- `Snowflake.Next` 当前已在线程内做同毫秒序列进位；补并发唯一性测试（如 8 goroutine × 10k）与时钟回拨测试，证明单调唯一。
- 实例注册：`ErrInstanceIDTaken` 时用新雪花 ID 重试（有限次数），而非直接 409；`ErrNodeTaken` 时刷新可用 node 列表重试（保持唯一约束兜底）。
- 测试：orchestration 单元/集成并发注册；snowflake 并发。
- 契约：无。

#### H9 CI 去 0.1 化

- 新增 `scripts/p8-static-check.ps1`：校验 4 项启动 env、0.2 文档链接、根 README 状态、openapi 版本、设置 85、迁移 42、compose 6 服务、无 0.1 env 键/0.1 门禁引用、无 secret、`.env` 未跟踪。
- `.github/workflows/ci.yml`：`static-acceptance` job 改为安装 pnpm 后运行 `scripts/p8-static-check.ps1`（内部跑 `frontend/scripts/validate-compose.mjs` 与 p2–p8 契约检查），删除 0.1 `check-ci.ps1`/`acceptance.ps1 -StaticOnly` 调用；test job 不变。
- 0.1 脚本不删除、不修改行为，只在文档标注 legacy；`check-ci.ps1`/`check-docs.ps1`/`acceptance.ps1` 不再被 CI 引用。
- 测试：本地 `pwsh -File scripts/p8-static-check.ps1` exit 0；对故意违规（版本串/0.1 键）失败自测。

#### H10 事件/审计保留清理

- 新增设置 `retention.event_days`（默认 90）；审计沿用 `security.audit_retention_days`（默认 90）。
- `audit` 与 `eventing` 各增加批量删除方法：`DELETE ... WHERE created_at < cutoff` 按批（默认 500，复用 `retention.gc_batch_size`）循环；event 删除按 `(created_at, channel, seq)`；先删子表（无），再删主表；单事务批量。
- leader 门控 worker：复用现有 GC 日循环（`retention.gc_interval_minutes`）与 leader gate；启动后先跑一轮；失败只记日志，不阻塞其他循环。
- 审计动作：每次清理写 `audit_log(action='retention.cleanup', payload={events_deleted,audit_deleted,cutoff})`（该审计行本身受保留期约束，可被后续清理）。
- 测试：单元 cutoff/批量；集成旧行删除、新行保留、审计行、事件游标回放对缺失事件返回 `stream.reset` 语义不被破坏；leader 门控。
- 契约：settings（83→85：`security.rate_limit_per_minute` + `retention.event_days`）；events.md 加 P8 审计 action；无新事件。

> 计数校准：P7 83 键 + `security.rate_limit_per_minute` + `retention.event_days` = **85**。

#### H11 限流

- 新增设置 `security.rate_limit_per_minute`（默认 `0`=关闭，整数 0–60000）。
- `server/ratelimit.go`：每实例全局令牌桶（rate=limit/60，burst=max(limit/10,1)）；键为实例级（非用户级），符合“按实例额度”；跳过 `/healthz`、`/api/health`、`/api/health/ready`、`/api/stream`、静态资源。
- 超限返回 429 JSON 错误信封 `code=rate_limited` + `Retry-After`（秒）；写 `X-RateLimit-Limit/Remaining`（可选）。
- 设置读取走 settings 热缓存；limit=0 直接放行；启动/更新后即时生效（同进程）。
- 测试：单元桶/跳过路径/429 头；集成 40 请求 30 限额 → 部分 429；limit=0 回归。
- 契约：settings、openapi 通用错误说明加 429；无新路径。

### 4.3 验收架构（模块化）

**推荐 A，否决 B/C**：

- A（采用）模块化脚本 + 聚合报告：故障域隔离、可并行开发、可单独复跑。
- B 单脚本：3–5k 行，失败定位/并行开发差，否决。
- C 只复用既有脚本：无法证明全旅程，否决。

| 脚本 | 覆盖 | 依赖 |
|---|---|---|
| `scripts/p8-acceptance.ps1` | S0–S6 全旅程（API/DB + 真实 LLM/Chrome + 双 SUT） | p8 克隆库、fixture、executor、renderer |
| `scripts/p8-resilience.ps1` | S9 长上下文/长执行、S10 硬化定向（设置热更/多执行器/PDF/版本/雪花/限流/保留清理） | p8 库、双 API、双 executor |
| `scripts/p8-multi.ps1` | S8 双副本/滚动/故障/SSE/真实离线重派 | p8 库、双 API、双 executor、共享 `.data` |
| `scripts/p8-capability.ps1` | S11 37 项 smoke/抽查 | p8 库、API |
| `scripts/p8-e2e.ps1` + `frontend/e2e/p8.spec.ts` | S12 浏览器全旅程关键页/全局页/截图 | p8 库、API、Web |
| `scripts/p8-static-check.ps1` | S13 文档/契约/CI/compose/secret | 无 DB |
| `scripts/p8-all.ps1` | 聚合：先 `p7-acceptance.ps1`（P9），再克隆 p8，按序跑上述脚本并汇总 | 全部 |

**证据**：`.tmp/p8-acceptance/`（scenarios.txt/acceptance.log/api-*.log/executor*.log/web*.log/screenshots）；**发布证据**：`docs/version-V0.2/acceptance/p8/{acceptance,resilience,multi,e2e,capability-signoff,static,known-limitations}.md` + `screenshots/` + `scenarios.txt`。

**原则**：验收脚本不修改产品代码；发现缺陷记为 FAIL 并继续剩余场景；修复后复跑；证据必须含命令、环境、数据、时间戳、退出码。

### 4.4 并行与写作用域

| 波次 | 任务 | 写作用域（互斥） |
|---|---|---|
| W0（Lead 串行） | 分支/spec/plan、迁移 #41/#42、设置两键、契约骨架、p8 contract check、wiring 骨架、基线测试 | `backend/internal/db/migrations/**`、`settings/application/catalog.go`、`contracts/**`、`cmd/eztest/**` |
| W1（6 路并行） | H1 / H2 / H3 / H4 / H5+H6+H8 / H10+H11 | 各自 `backend/internal/<ctx>/**` + 对应 `_test.go`；迁移 #41/#42 已由 W0 建好；不改 `cmd/eztest` |
| W2（并行） | 全旅程脚本、resilience、multi、capability、e2e、文档升级、静态检查/CI | `scripts/p8-*.ps1`、`frontend/e2e/**`、`docs/**`、`.github/workflows/ci.yml` |
| W3（Lead 串行） | wiring、server 中间件、实例面板、测试、契约冻结、真实验收、修复波、handoff、合并 | `cmd/eztest/**`、`server/**`、`contracts/**`、验收证据 |

共享文件（contracts、`cmd/eztest`、`server.New`、`p7-contract-check.cjs`、根 README）由 Lead 统一收口；子代理不并发写。

---

## 5. 数据模型与迁移

### 5.1 #41 `orchestration_0002_instance_inflight.sql`

```sql
ALTER TABLE api_instance
    ADD COLUMN active_requests INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN active_work     INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT ck_api_instance_active_requests CHECK (active_requests >= 0),
    ADD CONSTRAINT ck_api_instance_active_work CHECK (active_work >= 0);
```

- 只追加；不改 P7 已冻结列；`order.txt` 追加第 41 行。

### 5.2 #42 `execution_0005_case_run_sut_context_executor.sql`

```sql
ALTER TABLE case_run_sut_context
    ADD COLUMN executor_id BIGINT,
    ADD CONSTRAINT ck_case_run_sut_context_executor CHECK (executor_id IS NULL OR executor_id > 0);
```

- 逻辑引用 `executor_instance.id`，不建 FK（跨上下文规则）。
- 现有行 `executor_id` 为 NULL；新行为创建上下文时写入 `owner_executor_id`。
- 只追加；`order.txt` 追加第 42 行。

### 5.3 设置种子

- 无需迁移；`settings.SeedDefaults` 启动时插入缺失键。
- `retention.event_days` 默认 90；`security.rate_limit_per_minute` 默认 0。
- 既有库升级：P8 迁移只加列；设置种子幂等。

### 5.4 计数（P8 冻结时）

- 迁移 **42**（order.txt 42 行）；业务表 **85**（不新增表）；设置 **85**；事件 **118**（不新增）；任务 **19**；profiles **14**；openapi **162 paths / 200 ops / 264 schemas**。

---

## 6. API 契约

### 6.1 版本与冻结

- `info.version: 0.2.0-p8`；根 `x-frozen: P8`；`contracts/README.md` 增加 P8 冻结段。
- `/api/health` 运行时返回 `0.2.0-p8`。
- P8 只消费/追加：无新增 path/operation/schema；不改 P0–P7 既有语义。

### 6.2 追加字段

- `InstanceView`：`active_requests`（integer）、`active_work`（integer）。
- `GET /api/health/ready` data：`inflight_requests`（integer）、`inflight_work`（integer）。
- 错误信封新增 `rate_limited` 码；429 响应说明（通用错误/设置/限流路径）。
- 其余 schema 不变；字段均可选/有默认，旧客户端兼容。

### 6.3 文档/文本修复（非语义）

- openapi 文件级 stale 标记、P3 draft 注释、单 endpoint 描述更新为 P7/P8 现行状态。
- db-schema §5 表数量修正为 85 并补 P7 后总数；settings §2 计数修正为 85；settings §4g 明确启动读取项；task-types §4 删除重复保留；executor-protocol §10 与 §12 去重；P7 handoff 版本串不一致在 P8 handoff 注明（不改 P7 handoff 历史正文）。
- `p8-contract-check.cjs` 断言：openapi 版本/paths/ops/schemas；db-schema 42/85；settings 85；migrations 42；events 118；task 19；profiles 14；P2–P7 门禁前向兼容（`>=` 计数）。

---

## 7. 设置键（83→85）

| # | key | type | category | default | 说明 | secret | restart |
|---|---|---|---|---|---|---|---|
| 84 | `security.rate_limit_per_minute` | number | security | `0` | 整数 0–60000；0=关闭；每实例每分钟请求上限；总速率=副本数×限额 | 否 | 否 |
| 85 | `retention.event_days` | number | retention | `90` | 整数 1–3650；事件日志保留天数；审计复用 `security.audit_retention_days` | 否 | 否 |

- 两者均热加载；`RetentionWorker` 在下一轮日循环生效；限流在下一请求生效。
- `security.audit_retention_days` 不变（90）。

---

## 8. 事件与审计

- **不新增事件类型**；`settings.updated` 复用为跨实例缓存失效触发（已有事件，不改变 payload）。
- 新增审计 action：`retention.cleanup`（payload：`events_deleted`、`audit_deleted`、`cutoff`；由清理 worker 写入）。
- rate limit 不写事件（避免噪声）；429 只入访问日志/审计可选。
- SSE 语义不变：跨实例 `{channel,seq}`、`Last-Event-ID` 回放、`stream.reset`；事件清理后旧游标回放按既有 reset 语义处理。
- `executor-protocol` 多端点派发不改变事件/审计语义。

---

## 9. 任务、工具与 profile

- 不新增 work_item 类型/团队/profile/工具；任务类型仍 19，profiles 仍 14。
- `execute_case` 任务 payload 不变；路由所需 `executor_id` 从 `case_run.owner_executor_id`/`case_run_sut_context.executor_id` 解析。
- `knowledge` 注入 query 由 runner 派生，工具白名单不变。
- 多执行器路径仅影响 API 内部 `BrowserExecutor` 端口与组装，不改变 executor HTTP 协议/action。

---

## 10. 前端交付

- 设置页实例面板显示 `active_requests`/`active_work`（只读；drain 时可观察归零）；无新页面/路由。
- 版本详情 8 Tab、全局壳 8 项不新增；P8 不做 UI 大改。
- E2E：p6/p7 动态 ID；新增 p8 全旅程关键页/全局页/截图；不依赖固定 seed。
- 根 README 截图复用 P6/P7/P8 验收截图；不伪造 UI。

---

## 11. 测试与验收

### 11.1 测试层

- **单测**：H1–H11 每个包（settings/eventing/orchestration/server/agent/knowledge/execution/delivery renderclient/platform id）；限流桶；保留 cutoff；雪花并发。
- **集成**（`-tags=integration`）：迁移 #41/#42 幂等与列/约束；设置跨实例 Reload；heartbeat 计数；多执行器 fake 路由；保留清理删除/保留；审计 action；知识 FTS 排序；限流 429。
- **契约**：`node scripts/p8-contract-check.cjs`；P2–P7 门禁前向兼容；P7 检查改 `>=`。
- **静态**：`scripts/p8-static-check.ps1` + `frontend/scripts/validate-compose.mjs`。
- **E2E**：`frontend/e2e/p6.spec.ts`、`p7.spec.ts`、`p8.spec.ts`（真实 API/DB/Chrome；p8 不依赖固定 seed）。
- **真实验收**：`p7-acceptance.ps1`（P9）+ `p8-acceptance.ps1` + `p8-resilience.ps1` + `p8-multi.ps1` + `p8-capability.ps1`。
- **回归**：`scripts/test.ps1`（Go unit+integration、frontend lint/typecheck/test）、executor `pnpm test`、renderer `node --test`。

### 11.2 验收环境与数据

- 数据库：`eztest_v02_p8` 从 `eztest_v02_p7` 克隆；P7 库由 `p7-acceptance.ps1` 从 `eztest_v02_p6` 重建（若不存在）。
- 内容根：`.tmp/p8-acceptance/data`（evidence/reports/exports/objects/attachments）。
- 端口：API A 13333、API B 13344、renderer 13337、executor A 13345、executor B 13346、web 6868、PG 5433、fixture 14080/14081。
- 真实 LLM：克隆库设置中心的 DeepSeek `deepseek-flash`（沿用 `.env`/P7 克隆配置，验收前 `config test`）。
- 真实 Chrome：Playwright channel chrome；executor 启动后 `/healthz` 带 `browserVersion`。
- 双 SUT：`fixture-target.cjs` 两进程，`FIXTURE_VARIANT=orders`（14080）与 `payments`（14081）；页面/API/账号/目标库/代码仓按 P3/P5 fixture 绑定。
- 长上下文：临时 `context.window_tokens=8000`、`agent.tool_result_max_bytes=8192`；场景结束恢复 64000/32768。
- 多副本：API A/B 同库同 `.data`；snowflake node 1/2；leader advisory lock。
- 数据可复现：脚本记录 clone SQL、seed 命令、时间戳；每个场景独立版本/前缀，避免互相污染。

### 11.3 验收场景（S0–S13）

| 场景 | 内容 | 通过判据 |
|---|---|---|
| S0 | 启动/门禁 | 迁移 42、设置 85、真实 LLM config test、renderer health、executor A/B health、`/api/health/ready` 200、版本 `0.2.0-p8` |
| S1 | 管理员配置 | 创建双 SUT（family）/环境/页面/API/目标 DB/账号/代码仓；CRUD/校验/权限/secret 掩码 |
| S2 | 版本与绑定 | 创建版本；绑定双 SUT 环境/页面/API/DB/账号/代码仓/代码基线；默认 SUT；读回一致 |
| S3 | 真实 LLM 澄清 | 新建对话→≥3 轮→AI 产出 ≥2 draft 需求（含 1 跨系统）→用户编辑/单条确认→需求版本不可变；编辑后新 draft/stale 规则可验证 |
| S4 | 真实 LLM 设计 | 新建设计对话→AI 产出场景/用例（含 1 跨系统、1 故意失败用例）→确认→`case_version` current+confirmed；未确认不执行 |
| S5 | 真实 Chrome 执行 | 确认入队→波次→双执行器真实执行→跨系统用例两 SUT 上下文/证据→暂停/恢复→失败→问题候选；证据 sha256/归属正确 |
| S6 | 交付/收尾/重开 | 问题确认/合并/重开；波次+版本报告 HTML/PDF；链路节点/边；需求闭环；Version Steward 自动收尾→completed；重开→running；事件/审计 |
| S7 | P9 回归 | 运行 `p7-acceptance.ps1` S0–S12：PDF/模板、知识、GC、SSE、实例、离线重派、问题复发/as-of 全 PASS（或记录失败并修复） |
| S8 | 多副本/滚动/故障 | 双副本 ready；设置跨实例 ≤2s；drain 计数；滚动升级（B ready→drain A→计数归零→停 A→leader 移交）；kill -9 恢复；SSE 跨实例；真实 Chrome 执行器离线→`interrupted(executor_lost)`→epoch++→重派；无重复执行/不批量中断 |
| S9 | 长上下文/长执行 | 30+ 消息/窗口 8k：summary/pin/`context.compacted`、原文可展开、无终态 overflow；12 用例/2 波：tool 结果 head+ref、检查点恢复、暂停/恢复、无丢步骤 |
| S10 | 硬化定向 | 设置热更、多执行器各至少 1 run、PDF 10MiB 边界、API 版本、雪花 10k 唯一、限流 429+Retry-After、保留清理旧删新留+审计 |
| S11 | 能力矩阵 37 项 | 每行入口/证据/结论；P8 未覆盖行标 `PASS(prior)` 并给依据 |
| S12 | 浏览器 E2E | `/resources`→`/versions`→requirements→design→execution→issues→reports→chain→reopen；全局 `/knowledge`/`/quality`/`/agents`/`/issues`/`/reports`/`/settings`；≥20 截图；无固定 seed |
| S13 | 打包/文档/CI | `p8-static-check.ps1` exit 0；compose 静态校验；根 README/状态 0.2；0.1 横幅；无 0.1 门禁引用；Docker 未实跑已记录 |

### 11.4 关键场景细节

- **全旅程**（S3–S6）使用真实 LLM，不用 fake；LLM 输出不稳定时最多 2 次重试，仍失败记为产品/提示词缺陷并修复，不伪造数据。
- **跨系统用例**：步骤 1 在 orders 登录并校验订单金额；步骤 2 在 payments 登录并校验退款列表；断言分别带 `version_sut_id`；证据按 SUT 分组。
- **失败用例**：对 fixture 故意不一致的退款金额断言失败，触发问题候选/问题；用于问题确认/合并/重开与报告。
- **收尾**：等待 Version Steward 自动收尾（≤10 分钟）；若谓词不满足，检查原因并修复；不以直接改 DB/状态代理验收。
- **重开**：`POST /api/versions/{id}/reopen`（P4 契约）→ running + 事件/审计；重开后再收尾可复验。
- **长上下文**：通过 API 预置 30+ 用户消息（含长文本）再触发一次澄清 run；窗口 8k 强制 compaction；断言 `conversation_summary` 有摘要、pinned facts 保留、`context.compacted` 事件、run 非 `context_overflow` 终态；恢复窗口设置。
- **长执行**：API 创建 12 个确定性用例（含跨系统/慢页/大结果），确认→2 波；暂停 1 个恢复；大结果页面由 fixture 新增 `/p8/large?bytes=20000`（测试夹具，非产品）触发 tool 结果落盘；断言 head+ref、检查点续跑、无重复步骤。
- **真实离线重派**：executor B 跑慢页用例时停止 B；断言该 case_run `interrupted(executor_lost)`、`dispatch_epoch++`、重派到 A 成功；同波其他 run 不受影响；不以合成行代替。
- **滚动升级**：A/B 同时在线→B ready→drain A→A readiness 503 且计数下降→计数 0→停 A→B 接管 leader→无重复 task/run/case_run→迁移只一次。
- **设置热更**：A 更新 `retention.gc_dry_run`/`security.rate_limit_per_minute`；B 在 ≤2s 内 GET 到新值/行为变化。
- **限流**：设 30/min；40 次并发请求→至少 1 个 429 且带 `Retry-After`；恢复 0 后请求 200。
- **保留清理**：插入 >90d 的 event/audit 行与近期行；触发清理；旧删新留；`retention.cleanup` 审计；游标回放 reset 语义。
- **多执行器**：`execution.executor_urls` 含 B；两个 case_run 的 `owner_executor_id` 分别指向 A/B；各自 executor 日志收到 context/action；单端点回归。
- **PDF 边界**：单测 10 MiB±1；不生成 10 MiB 级真实 PDF（成本/时间），契约注明。
- **雪花**：单测/集成 10k 并发唯一；实例注册并发不冲突/不误 409。
- **E2E**：真实 API/DB/Chrome；不触发真实 LLM（旅程已由 S3–S6 覆盖），页面数据来自 S0–S6 验收库；截图 fullPage/viewport/element 三 helper。
- **静态/打包**：`validate-compose.mjs` + `p8-static-check.ps1`；Docker 未安装，明确“仅静态校验、未实跑”。

### 11.5 能力矩阵签字

- 文件：`docs/version-V0.2/acceptance/p8/capability-signoff.md`，37 行（A1–I2）。
- 每行列：能力、0.2 入口、P8 证据（S 场景/脚本/截图/接口）、既往证据（P0–P7 handoff/acceptance）、结论（PASS / PASS(prior) / FAIL）。
- 规则：P8 全旅程覆盖 A/C/D/E/F/G/H 核心路径；P9 行用 P7 验收；B4/D3/G2 等用 P5/P6 证据 + P8 入口 smoke；I1/I2 用测试/静态证据。
- 签字：Lead 代用户签字；用户审阅 handoff 后可要求复验。

### 11.6 缺陷分级与门禁

| 级别 | 定义 | 门禁 |
|---|---|---|
| P0 | 数据丢失/损坏、越权、重复执行、核心旅程完全阻断 | 必须修复；未修复 = 交付阻断 |
| P1 | 核心旅程功能失败但有绕过、契约破坏、验收不可复现 | 必须修复；未修复 = 交付阻断 |
| P2 | 非核心功能缺陷、UI 异常、性能明显退化 | 记录处置；可带已知限制交付 |
| P3 | 文案/样式/体验 | 记录 backlog |

- 交付时无未关闭 P0/P1；P2 在 handoff §4 列表；任何“临时降级”必须显式记录并获用户批准。
- 验收脚本不得通过改产品代码/直接写 DB 绕过失败；修复必须走后端/前端变更 + 回归。

---

## 12. 契约冻结与文档变更

### 12.1 契约

- `openapi.yaml`：`0.2.0-p8`、`x-frozen: P8`；无新增 path/op/schema；加 InstanceView/ready 字段与 429 说明；修复 stale 文本。
- `db-schema.md`：`api_instance` +2 列、`case_run_sut_context.executor_id`；表数 85；迁移 #41/#42。
- `migrations-order.md`：§10 P8（#41/#42，`order.txt` 42）。
- `settings.md`：§4h（83→85，2 键）。
- `events.md`：§17 P8 审计 action `retention.cleanup`；无新事件。
- `executor-protocol.md`：§12.3 多端点派发改 P8 已实现；线协议仍 v1.2。
- `task-types.md`/`agent-profiles-tools.md`：知识 query 派生说明；任务/工具计数不变。
- `contracts/README.md`：P8 冻结状态与计数。
- 新增 `scripts/p8-contract-check.cjs`；`p7-contract-check.cjs` 改前向兼容 `>=`。

### 12.2 文档

- `README.md` / `README.zh-CN.md`：改写为 0.2（状态、能力、架构、快速启动、验收状态、已知限制、文档地图、License）；不再宣称 0.1 的 29/29 验收等旧口径；明确 Docker 未实跑。
- `DEVELOPMENT_STATUS.md`：改写为 0.2 状态摘要（P0–P8、测试/契约/迁移/设置/验收证据）；0.1 长历史移入 `docs/version-V0.1/DEVELOPMENT_STATUS.md`。
- `docs/version-V0.1/README.md`：0.1 归档索引。
- `docs/architecture-design.md`、`detailed-design.md`、`deployment.md`、`operations.md`、`user-guide.md`、`development.md`、`openapi/api.yaml`、根 `acceptance.md`：顶部加“0.1 归档，0.2 见 docs/version-V0.2/ 与 deploy/README.md”横幅；不重写 0.1 内容。
- 新增 `docs/version-V0.2/acceptance/p8/runbook.md`：0.2 本地开发/验收/多副本运行入口（引用 deploy/README、scripts、.env 4 项）；作为 P8 验收证据交付，不新增设计目录文件。
- `deploy/README.md`：补充 P8 版本/实例计数/限流/保留清理说明；保留“Docker 未实跑”声明。
- 验收文档：`docs/version-V0.2/acceptance/p8/`（见 §4.3）。
- `phase-08-handoff.md`：按 `contracts/handoff-template.md` 六节 + 最终交付说明、证据索引、已知限制、0.3 backlog、运行/部署入口。

### 12.3 不修改

- `docs/version-V0.2/architecture-design.md`、`high-level-design.md`、`research/*`、`ui/*`、`phases/phase-0X-*`（phase-08 handoff 除外）、`README.md`（version-V0.2 设计索引）。
- 0.1 设计文档正文（只加归档横幅）。
- 已冻结迁移（只追加 #41/#42）。
- 用户未跟踪文件 `docs/用户专用文件，禁止AI阅读.md`（不读、不改、不提交）。

---

## 13. 风险与回滚

| 风险 | 对策 |
|---|---|
| 真实 LLM 非确定性导致澄清/设计不稳 | 结构化提示 + 最多 2 次重试；失败记录缺陷并修复；不以 fake 数据替代 |
| 全旅程耗时长/端口冲突 | 分脚本并行；固定端口表；每场景独立版本前缀；脚本负责进程清理 |
| 多执行器改动触及 P7 冻结语义 | 用户已批准（P8-D-11）；契约显式转正；单端点回归；fake + 真实双测试 |
| drain 计数跨上下文耦合 | 只读计数端口；不写他域表；nil 时降级 0；集成测试覆盖 |
| 事件清理破坏 SSE 回放 | 保留 `stream.reset` 语义；只删 >90d；游标回放测试 |
| 限流误伤验收/CI | 默认 0 关闭；验收临时开启；跳过 health/SSE |
| 文档大改引入虚假声明 | README 只写可验证事实；Docker 未实跑/跳过 SLO/安全审计明确写出 |
| 0.1 资产误用 | 静态检查禁止 CI 引用；p8 脚本名独立；文档标注 legacy |
| E2E 固定 seed 复发 | 动态发现 + 禁止默认 ID；p8 spec 自建数据 |
| 收尾 Agent 不自动关闭 | 等待上限 10min；失败按缺陷修复；不以 DB 直改代理 |
| 回滚 | 迁移只追加；限流/清理可设 0/关闭；多执行器可回退 `executor_urls=""`；代码按分支回滚 |

---

## 14. Backlog（0.3 / 后续，不实现）

- 跨实例设置失效的更强一致性（版本号/DB 校验）；限流按用户/租户维度、Redis 共享桶。
- 事件日志归档/冷存储；审计导出；保留期 UI。
- 多执行器 per-run 容量预留/亲和性；执行器远程 browser/VNC、storage_state 复用。
- 收尾 Agent 的人工干预面板；版本关闭按钮。
- 0.1 数据导出脚本；0.1 harness 清理/删除。
- 报告 diff/自定义模板/多模板；知识向量检索/冲突合并；跨版本问题合并。
- SLO/告警（按设计“后置/按需”，本阶段用户明确跳过）。
- 安全审计/生产级加固（按用户裁定不做）。

---

## 15. 自审记录

- **占位符**：无 TBD/TODO；所有设置默认、迁移号、场景、判据已定。
- **一致性**：设置计数 83→85；迁移 40→42；表 85；事件 118；openapi 版本 p8；与 §5/§6/§7 一致。
- **范围**：仅硬化/验收/文档/契约；不新增功能；SLO/安全审计/fake-LLM/Docker 实跑明确排除并记录。
- **歧义**：限流默认 0（关闭）已在 P8-D-13 明确；事件保留 90d 在 P8-D-14；多执行器语义转正已获批准；收尾/重开以真实 Agent/API 验证，不用代理。
- **前置**：P7 完成、contracts frozen P7、真实 LLM/Chrome 可用、无 Docker；P7 库可重建。
- **2026-11-02 技术修订**：H2 的 `active_work` 改为进程内 `platform/load.Counter`（agent run 入口 Inc/退出 Dec），不跨上下文读表；DB 列/API 字段/验收判据不变。
