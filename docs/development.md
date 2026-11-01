> **0.1 归档；0.2 权威文档见 [docs/version-V0.2/](version-V0.2/README.md) 与 [deploy/README.md](../deploy/README.md)。**

# EzTest 开发指南（P8）

> 读者：参与 EzTest 后端/前端/executor/测试的开发与评审人员。
> 相关文档：[系统设计蓝图](design/system-blueprint.md)、[前端设计](design/frontend-design.md)、[阶段规划](design/implementation-phases.md)、[部署指南](deployment.md)、[运维手册](operations.md)、[用户手册](user-guide.md)。
> 实现约定以 [`Agent.md`](../Agent.md) 为准；本文件只汇总开发入口与 P8 新增约束。

## 1. 技术栈与架构

| 层 | 技术 | 说明 |
|---|---|---|
| API | Go 1.24（go.mod） | 标准库 `net/http` 方法路由 + `pgx/v5`；启动时自动迁移。 |
| 数据库 | PostgreSQL 17 | 只支持 PostgreSQL；advisory lock 用于迁移与多实例单活。 |
| Web | Next.js 16 App Router、React 19、TypeScript、Tailwind 4 | Radix 原语、`next-themes`、React Flow（`@xyflow/react` 12）。 |
| Executor | Node 22 + Playwright | 独立进程，Bearer token 保护；协议 A'。 |
| 测试 | Go test、Vitest/Testing Library、Playwright、node --test | E2E 使用本机 Chrome/Edge channel，不下载 Chromium。 |
| 质量 | Biome、axe-core、YAML 解析、评测/ SLO/安全脚本 | 静态验收 + 真实浏览器 + gated 真实 LLM。 |

### 1.1 限界上下文（DDD）

```
identity  eventing  llm  storage  audit  agent  resource
version  codeintel  mindmap  execution  delivery
P8 新增：chain（只读读模型） search（只读聚合） workspace（待办聚合） quality（评测）
P8 扩展：version（知识） execution（GC/视觉/执行器硬化） codeintel（版本变更/事实修正/质量）
        server（限流/安全头/路由装配） platform/leader（advisory lock）
```

约束（Agent.md §6.2）：

- 每个上下文四层：`domain` / `application` / `infrastructure` / `interfaces/httpapi`。
- 跨上下文只经 application 端口（Provider/Adapter）；`chain`/`search`/`workspace` **不直接读他域表**，只消费各上下文提供的只读 Provider。
- `quality` 只读 `quality.eval_run`/`eval_metric`；评测 runner 通过 HTTP API 写入，不直接写业务表。
- `cmd/eztest` 是唯一组装根：读配置 → 连库 → 迁移 → 种子 → 装配 workers/HTTP → 优雅退出。
- `internal/server` 只装配路由/中间件；`internal/platform` 放错误信封、分页、请求 ID、AES keyring、幂等、leader 原语。

### 1.2 请求链与关键中间件

```
recover → request ID → logging → CORS → auth → rate limit → idempotency → mux
```

- 认证：Bearer token，仅存 SHA-256 哈希；`/healthz`、`/api/health`、`/api/openapi.yaml`、`/api/auth/login` 公开。
- 限流：每用户/IP 内存令牌桶 + SSE 并发；超限 429 `rate_limited` + `Retry-After`。
- 幂等：写接口可选 `Idempotency-Key`；同 key 同 body 重放，不同 body 409。
- 错误：统一 `{"error":{code,message,request_id,retryable,details}}`；`details.reason` 是稳定原因码。
- ID：P3/P4/P5/P6/P7/P8 自有 ID 一律十进制字符串（D29），禁止 JS `Number()` 解析。

### 1.3 P8 新增模块与端口

| 模块 | 职责 | 关键端口 |
|---|---|---|
| `internal/chain` | `GET /api/versions/{id}/exploration-chain`：需求/用例/最新结果/问题/影响面的只读聚合；节点预算与截断标记 | `ChainProvider`（version/mindmap/execution/delivery/codeintel） |
| `internal/search` | `GET /api/search`：版本/需求/用例/问题分组搜索，按类型上限，Provider 失败降级 | `SearchProvider` |
| `internal/workspace` | `GET /api/todos`：待评审 + 管理员待接管/gate；30s 轮询由前端负责 | `ReviewProvider`/`ExecutionProvider` |
| `internal/quality` | 评测 run/指标持久化与查询；确定性阈值+基线回归 | `RunRepository`、`IDGenerator`、`Clock` |
| `platform/leader` | `pg_try_advisory_lock` 会话级锁；调度/自主/维护三个派生 key | `Try(ctx,key) (release,ok,err)` |
| `execution/evidence` | GC 候选/保护/SHA 校验/purge 元数据；默认 dry-run | `GCService` |
| `version/knowledge` | 草稿→审核→批准→注入；版本内闭环 | `KnowledgeRepository`、`KnowledgeInjector` |
| `codeintel/fact review` | 事实人工修正 + 质量聚合；`version_change` 快照 | `FactReviewService`、`RepoQualityService` |

## 2. 本地环境

### 2.1 工具链

- Windows 10/11，PowerShell 5.1+（脚本兼容 `powershell.exe`）。
- Node.js 22+、pnpm 10/11（容器固定 `pnpm@11.28.5`）。
- 仓库可自带便携 Go 1.24 + PostgreSQL 17（`.tools/`，gitignored）。

```powershell
Copy-Item .env.example .env
# 编辑 .env：数据库密码、EZTEST_SECRET_KEYS、管理员密码、executor token 等
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .tools/setup-toolchain.ps1
# 或已有工具链：
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/db.ps1 start
```

数据库固定端口 `5433`：`eztest` 开发库、`eztest_test` 集成测试库；测试库名必须以 `_test`/`_e2e` 结尾且不得与开发库相同。

### 2.2 启动

```powershell
# API（自动迁移/种子管理员；13333）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-api.ps1
# Web（6868；首次先在 frontend 执行 pnpm install）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-web.ps1
# executor（执行用例时）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-executor.ps1
```

底层等价：API 必须在 `backend/` 启动并显式设置 `EZTEST_OPENAPI_PATH`（相对路径按进程工作目录解析）；Web 在 `frontend/` 执行 `pnpm dev`。

## 3. 测试命令

### 3.1 快速门禁

```powershell
# Go 单元 + Go 集成 + 前端 lint/typecheck/test
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1

# 只跑某上下文（单元）
cd backend
..\.tools\go\bin\go.exe test -count=1 ./internal/chain/... ./internal/search/... ./internal/workspace/... ./internal/quality/...
# 集成（需 EZTEST_TEST_DATABASE_URL）
..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/chain/... ./internal/quality/... ./internal/execution/...
```

### 3.2 前端

```powershell
cd frontend
pnpm lint          # Biome
pnpm typecheck     # tsc --noEmit
pnpm test          # Vitest
pnpm build         # Next standalone 构建
pnpm e2e           # Playwright（推荐用 scripts/e2e.ps1 管理服务/DB/fake LLM）
```

前端测试环境注意 React Flow 需要 `ResizeObserver`/`DOMMatrixReadOnly` 等 polyfill（`vitest.setup.ts`）；键盘/ARIA 回归由 axe 与自有 roving focus 测试覆盖。

### 3.3 executor

```powershell
cd executor
pnpm test          # node --test，不依赖数据库
```

executor 的 WebSocket 白名单由 `EZTEST_EXEC_ALLOWED_HOSTS` 控制（默认 `localhost,127.0.0.1`）；远程目标需显式设置，且 WS 还必须同时通过 context `allowedHosts`。HTTP 导航/请求不受该白名单影响。

### 3.4 E2E

```powershell
# 全量（自行启动/停止 API、Web、fake LLM、fixture target，使用 eztest_e2e 库）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1

# 单 spec
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 exploration-chain
# 或：cd frontend; pnpm exec playwright test e2e/p8-exploration-chain.spec.ts
```

P8 相关 spec：`frontend/e2e/p8-exploration-chain.spec.ts`、`mobile-nav.spec.ts`、`agent-console.spec.ts`、`code-repos-index.spec.ts`；P7 的 continuing/issues/report/supplement/degraded 等 spec 仍在回归集内。

E2E 约定：

- 使用本机 Chrome/Edge channel；不联网下载浏览器。
- fake LLM 由 `scripts/fake-llm.cjs` 提供（默认 `127.0.0.1:13334`），E2E 零真实费用。
- fixture target `scripts/fixture-target.cjs`（默认 13335）用于登录/页面执行。
- 失败保留截图/trace（以 Playwright 配置为准）；不要提交 `.tmp/`。

### 3.5 静态验收

```powershell
# 文档/OpenAPI/README/env/Compose/迁移/fake-LLM 静态契约
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly

# CI 结构检查
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/check-ci.ps1
```

静态验收需要 Node、`frontend/node_modules/yaml` 与 git；不需要数据库/API/Playwright。`acceptance.ps1` 会解析 OpenAPI YAML、从 `migrations/order.txt` 动态推导迁移/表清单，并检查 P1–P7 的端点/事件/reason/testid 关键词与 P8 评测语料/基线；P8 端点/事件的脚本级断言仍待后续接入（OpenAPI 已同步，需补 `acceptance.ps1` 检查）。

## 4. 评测（Eval）

语料与基线唯一来源：`eval/`（`corpus/`、`ground-truth/`、`baseline.json`），维护流程见 `eval/README.md`。

```powershell
# 静态自检（语料/基线/DB 目标/工具链；不启动服务）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -DryRun

# 确定性门禁（fake LLM + fixture executor；失败非 0）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1

# 真实 LLM 报告（gated；需 EZTEST_RUN_REAL_LLM=1 与 llm_*）
$env:EZTEST_RUN_REAL_LLM = "1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -RealLLM
```

- 六个指标：`design_coverage`、`impact_recall`、`impact_precision`、`classification_accuracy`、`issue_false_positive_rate`、`supplement_hit_rate`。
- 视觉分类指标：`visual_classification_accuracy`（阈值 1.00）。`scripts/eval.ps1` 的预检阶段用 `go run ./internal/execution/evalvisual` 把 `eval/ground-truth/visual.json` 重放进生产 `domain.ClassifyVisual`，`-DryRun` 同样执行并在准确率不足时失败；结果写入 `observation.visual`/`metrics.visual_classification_accuracy`，与六个 §9.1 指标并存。
- 确定性轨道：阈值 + 基线回归（任一指标比 `baseline.json` 差 >0.05 失败）；真实 LLM 轨道只报告，不阻断。
- 结果写入 `.tmp/p8-eval/`，并通过 `POST /api/quality/eval-runs`（admin）写入数据库；质量中心只读展示。
- 修改 fake LLM 输出/语料/真值必须同步提升 `corpusVersion` 并更新基线，在提交说明中给出运行命令与结果。

## 5. SLO

```powershell
# 静态预检
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun
# 真实测量（需 API/Web/浏览器；可选）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 `
  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868
# 链路专项 200 节点/400 边
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo-chain.ps1 `
  -ApiBaseUrl http://127.0.0.1:13333 -WebBaseUrl http://127.0.0.1:6868 `
  -DatabaseUrl $env:EZTEST_DATABASE_URL
```

报告：`docs/acceptance/p8/slo.md`、`docs/acceptance/p8/slo-chain.md`。SKIP 不等于 PASS；当前全量 SLO 只有 dry-run 静态 PASS，最终测量待 Task 35 验收。

## 6. 视觉断言（D62）交付状态

**已接线**（Task 25 收口提交：迁移、mindmap 服务、executor、前端之外，另含 `main/config` 装配、`execution.visual_assert` 工具、四态 finish 矩阵与 eval runner）：

- 数据/API：`mindmap_0003_visual.sql`（`case_visual_assertion` + `visual` kind）、`execution_0005_visual_evidence.sql`（`visual_baseline/visual_actual/visual_diff` 证据 kind）；`PUT /api/versions/{versionId}/mindmap/assertions/{assertionId}/visual`（登录用户，节点 revision OCC）与 `POST .../visual/baseline`（admin 审批）。
- 执行：executor `visual.compare`（`pixelmatch`+`pngjs`）；Go `CompareVisual`/`ClassifyVisual` 四态；`execution.visual_assert` 工具（`execution-agent` profile 启动时幂等补工具与提示词）；环境不符 → `inconclusive(environment)`，`ValidateFinish` 仅在该 visual mismatch 存在时接受该组合。
- 配置：`EZTEST_VISUAL_BASELINE_MAX_BYTES`（默认 5 MiB，API 范围 64 KiB–20 MiB）已被 API config 与 executor 读取；`EZTEST_VISUAL_DIFF_THRESHOLD` 仍未读取，阈值保存在每条断言（0–0.1，默认 0.005）。
- 评测：`eval/corpus/visual.json` + `eval/ground-truth/visual.json` 已接入 `scripts/eval.ps1` 预检，`-DryRun` 也执行 `visual_classification_accuracy`。
- 文档：OpenAPI 已发布两条 visual path、`visual` 断言字段、三类证据 kind、`case.visual_baseline_approved` 事件与错误 reason；README/用户手册/运维/发布说明已同步。
- **仍待真实验收**：真实 Chromium 的 Go→executor 端到端比较、浏览器操作与截图证据由 Task 34/35 执行；当前单测/评测通过不代表真实环境已验收。数据库会存在 `case_visual_assertion` 表与 visual evidence kind，属预期。

## 7. OpenAPI 与文档同步

- OpenAPI 是**手工维护**的：`docs/openapi/api.yaml`，服务端 `GET /api/openapi.yaml` 直接返回该文件（路径由 `EZTEST_OPENAPI_PATH` 指定）。
- 新增/修改 API 时必须同步：
  1. path + operationId + 权限/错误响应（401/403/404/409/422/429/500/503）；
  2. `components/schemas`（ID 类型、nullable、枚举、分页信封）；
  3. `VersionEventType`/`ErrorEnvelope`/`HealthEnvelope` 等总表；
  4. `scripts/acceptance.ps1` 的关键词检查（若新增 P8 契约）与 README 端点表。
- 文档/契约自检：`scripts/check-docs.ps1` 断言四类 P8 文档存在、README/`.env.example` 环境变量键、OpenAPI YAML 与 18 个 P8 关键 path、Compose 四服务/六卷/健康检查/卷挂载/Dockerfile、本地 Markdown 链接与秘密扫描；已接入 `scripts/acceptance.ps1 -StaticOnly`（`-Dod P8`）。新增/删除 P8 path 或文档时同步更新该脚本。
- 约定：成功 `{"data":...}`；列表用 `next_cursor`/`has_more` 或前端专用分页；错误统一信封；写接口支持 `Idempotency-Key`；P3+ ID 十进制字符串。
- 修改后至少做 YAML 解析：
  ```powershell
  cd frontend
  node -e "const YAML=require('yaml');const fs=require('fs');const d=YAML.parse(fs.readFileSync('../docs/openapi/api.yaml','utf8'));console.log(Object.keys(d.paths).length)"
  ```
  以及 `scripts/acceptance.ps1 -StaticOnly`。

## 8. 迁移：append-only

规则（强制）：

1. 迁移文件在 `backend/internal/db/migrations/`，命名 `<context>_<version>_<desc>.sql`；执行顺序以 `order.txt` 为准，**新迁移追加到末尾**。
2. 已应用迁移**不可修改/删除/重排**；`schema_migrations` 记录版本与校验和，改动会让启动/验收失败。
3. 只做加法式 DDL（新表/新列/放宽约束/新索引）；避免破坏性变更。需要回退时用备份恢复，不写 down migration。
4. 启动时在 advisory lock 下按顺序事务执行；重复启动幂等；`scripts/acceptance.ps1` 从 `order.txt` 动态推导条数，不要写死数量。
5. 新迁移必须同步：README/设计文档的数据模型、OpenAPI（如暴露新字段）、评测/验收脚本、备份恢复的目录/表说明（如新增存储）。
6. P8 迁移示例：`codeintel_0006_version_change.sql`、`codeintel_0007_fact_review.sql`、`version_0006_knowledge.sql`、`quality_0001_eval.sql`、`execution_0004_evidence_purge.sql`、`mindmap_0003_visual.sql`、`execution_0005_visual_evidence.sql`。视觉迁移已提交并随启动执行，视觉 API/执行入口已接线（见 §6）。

## 9. 依赖与许可治理

- 前端运行时依赖必须为 MIT/Apache-2.0 等宽松许可；新增依赖先确认许可、SSR/测试环境兼容、包体积与安全记录。
- 当前 P8 新增：`@xyflow/react` 12.12.0（MIT，仅链路画布懒加载）；`axe-core` 4.14.0 为 **dev-only MPL-2.0 例外**（已在 `scripts/security-audit.ps1` 登记，进入生产依赖会 FAIL）。
- executor 依赖 `playwright`（Apache-2.0）与视觉断言的 `pixelmatch`/`pngjs`（MIT，已提交）；新增依赖需登记并在 `pnpm audit`/许可清单中通过。
- 锁文件（`frontend/pnpm-lock.yaml`、`executor/pnpm-lock.yaml`）必须提交；升级依赖后跑 `pnpm audit --prod --json` 与前端构建。
- 安全/许可审计：
  ```powershell
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1 -SkipLive
  # 有 API 时可去掉 -SkipLive 做实时 403/200 权限矩阵
  ```
  结果记录在 `docs/acceptance/p8/security-audit.md`；`govulncheck` 缺失记为 SKIP，不假装通过。
- 秘密：`.env`、`.tools/`、`.tmp/`、`.data/`、`node_modules` 不入库；提交前跑秘密扫描/`git diff` 自查。

## 10. CI 与合并前检查

`.github/workflows/ci.yml`：

1. Go 单元 `go test -count=1 ./...`、集成 `-tags=integration ./...`（服务 PostgreSQL）。
2. 前端 `pnpm install --frozen-lockfile`、`lint`、`typecheck`、`test`。
3. executor `pnpm install`、Playwright Chromium、`pnpm test`。
4. 静态验收 `scripts/check-ci.ps1` + `scripts/check-docs.ps1` + `scripts/acceptance.ps1 -StaticOnly`。

CI **不跑** gated 真实 LLM、真实浏览器 E2E 与 SLO/备份演练；这些以本地/定时验收为准。合并前至少：

- [ ] 相关单元/集成测试通过，未破坏既有 P0–P7 路径；
- [ ] 前端 lint/typecheck/test/build 通过（涉及 UI 时）；
- [ ] `scripts/acceptance.ps1 -StaticOnly` 通过；
- [ ] OpenAPI/README/env/迁移/Compose 已同步；
- [ ] 无秘密、无 `.tmp`/构建产物入库；
- [ ] 行为变化写入 `docs/release-notes/p8.md`；
- [ ] 已知 SKIP/未验证项明确记录，不写成“全部完成”。

## 11. 相关文档

- [系统设计蓝图](design/system-blueprint.md)（P8 决策 D53–D69 与实现注记）
- [前端设计](design/frontend-design.md)（链路画布/壳/质量/知识）
- [实施阶段](design/implementation-phases.md)（P8/P9 状态）
- [部署指南](deployment.md) / [运维手册](operations.md) / [用户手册](user-guide.md)
- [P8 发布说明](release-notes/p8.md)
- `eval/README.md`、`docs/acceptance/p8/` 下的 SLO/备份/安全证据
