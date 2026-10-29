# P8 安全审计（Task 32）

- 日期：2026-10-29
- 分支：`dev`
- 审计基线：`94ea509` + Task 32 提交 `1b8d6f9` + fix round 1 加固（本文件、`scripts/security-audit.ps1`、`frontend/next.config.ts`）
- 环境：Windows，Windows PowerShell 5.1.26100.7462，Node `v24.18.1`，pnpm `11.28.5`，Go `go1.24.6`（仓库内 `.tools/go`），本机未安装 Docker
- 范围：秘密扫描（含构建产物）、Go 漏洞扫描、Node 生产依赖漏洞、权限矩阵、依赖许可清单、Next 安全响应头
- 结论：**PASS**（`-SkipLive`：32 PASS / 2 SKIP / 0 FAIL，退出码 0）。无阻塞发现；两个 SKIP 为工具缺失与未启动 API，均不阻塞。
- 快照说明：结果在**共享工作区**采集，并行任务的未提交文件会改变扫描计数（尤其是秘密扫描文件数）；此快照只代表采集时的树。W3 gate 需在完整分支上重跑本文件 §1 的命令并更新快照。

## 1. 执行命令

```powershell
# 静态权限矩阵 + 全部离线检查（本次验收命令）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1 -SkipLive

# 需要实测权限矩阵时：先启动 API，再执行（默认读取 .env 的 EZTEST_HTTP_ADDR）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1
# 或显式指定 API
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1 -ApiBaseUrl http://127.0.0.1:13333

# 安全头配置不破坏构建
cd frontend; pnpm build
```

脚本退出码定义：只有 `FAIL` 返回 1；`SKIP` 不阻塞并写入报告；秘密与凭据值永不打印。

## 2. 结果快照（2026-10-29，fix round 1，`-SkipLive`）

| 检查 | 结果 | 说明 |
|---|---|---|
| 仓库秘密扫描 | PASS | 2647 个文件（2163 源文件 + 484 构建产物文本），跳过 21 个二进制、2 个 >2 MiB、2 个 `.env*`；0 个私钥/Provider Token/硬编码凭据 |
| `.env` 未入库 | PASS | `git ls-files .env` 为空，且 `git check-ignore` 命中 |
| `govulncheck ./...` | **SKIP** | 本机有 Go 1.24.6，但未安装 `govulncheck`；安装命令已写入脚本提示 |
| frontend `pnpm audit --prod --json` | PASS | JSON 结果 `metadata.vulnerabilities` 全 0、`advisories` 为空 |
| executor `pnpm audit --prod --json` | PASS | JSON 结果 `metadata.vulnerabilities` 全 0、`advisories` 为空 |
| 静态权限矩阵 | PASS | 见 §4；admin 路由 13/13、`/api/resources/*` 写/动作 34/34、只读路由无 per-route guard、16 个 helper 注册全部识别 |
| 实时权限矩阵 | **SKIP** | 审计时 API 未启动；按约定静态断言通过并记录 SKIP（启动 API 后去掉 `-SkipLive` 即实测） |
| 依赖许可清单 | PASS | 27 个直接依赖；`axe-core` 以 **dev-only** `MPL-2.0` 例外登记（scope 不符会 FAIL） |
| Next 安全头 | PASS | 5 个响应头均配置；CSP 为 Report-Only；catch-all 条目返回 `securityHeaders` |
| `pnpm build` | PASS | `next build` 成功，13 个页面/路由生成 |

汇总：**32 PASS，2 SKIP，0 FAIL**。计数会随共享工作区中并行任务的文件变化；W3 gate 重跑后以新快照为准。

### 2.1 RED/GREEN 证据（安全头与构建产物秘密扫描）

- 配置前运行脚本：`X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options`、`Permissions-Policy`、`Content-Security-Policy`、`headers()` 共 6 项 FAIL，退出码 1。
- 在 `frontend/next.config.ts` 增加 `headers()` 后同一命令：6 项全部 PASS，退出码 0。
- **fix round 1 负向 fixture（证明检查不是空转）**：

| fixture | 结果 |
|---|---|
| `.next/static` 写入形状匹配的假 token 文件 | 修复前 PASS（漏报）；修复后 FAIL，退出码 1；删除文件后恢复 PASS |
| `headers()` 返回 `headers: []` | 修复前 PASS（假绿）；修复后 FAIL（`securityHeaders reference=False`），退出码 1 |
| `headers()` 返回 `[]` | 修复后 FAIL（`catch-all source=False`），退出码 1 |
| 注释掉 `securityHeaders` 引用 | 修复后 FAIL（注释被剥离，引用不存在），退出码 1 |
| `/api/resources/systems` 的 POST 改为无 guard | FAIL（`unguarded: "POST /api/resources/systems"`），退出码 1 |
| 额外创建 `frontend/next.config.js` | FAIL（多个 Next 配置歧义），退出码 1 |
| `axe-core` 移到生产依赖 | FAIL（`MPL-2.0 例外登记为 dev，实际 scope=prod`），退出码 1 |

- 构建后用 `next start -p 6869` 实测 `GET /login`（200），响应头实际下发（本次 fix round 未改 `next.config.ts`，沿用该证据）：

```
X-Content-Type-Options: nosniff
Referrer-Policy: strict-origin-when-cross-origin
X-Frame-Options: DENY
Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=(), usb=(), browsing-topics=()
Content-Security-Policy-Report-Only: default-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; img-src 'self' data: blob:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; connect-src 'self' ws: wss: http://localhost:13333 http://127.0.0.1:13333
Content-Security-Policy: <absent>
```

CSP 使用 `Content-Security-Policy-Report-Only` 的原因：Next App Router 运行时与 Tailwind 会注入内联样式/脚本，直接强制会破坏页面。待 report-only 违规基线清零后再切换为 enforcing，且不能只改 key 名：必须为 Next 运行时脚本引入 **nonce 或 hash**（通常经 middleware/headers 为每个请求生成 nonce 并传给 `<Script>`），为内联样式引入 **style nonce/hash** 后再移除 `'unsafe-inline'`；`'unsafe-eval'` 只应保留在开发/测试配置中，生产 enforcing 前需移除。`X-Frame-Options: DENY` 与 `frame-ancestors 'none'` 同时覆盖旧/新浏览器。

## 3. 秘密扫描（含构建产物）

- 扫描工作树（含未跟踪、未被忽略的文件）**以及文本类构建产物**：
  - `.next/static/**`（JS/CSS/JSON/map 等）、`.next/server/**` 的文本文件；
  - 仓库内任意位置（node_modules 等运行时目录之外）的 `dist/**`、`out/**`、`coverage/**`；
  - 目的：防止 secret 只出现在下一构建产物的打包/内联结果中而源树干净。`.next/standalone`、`.next/cache`、`.next/dev` 等运行时副本不扫描。
- 排除：`.git`、`.tools`、`.tmp`、`.data`、`node_modules`、`.pnpm-store`；`.env*` **按 basename 在任意层级排除**（`.env`、`.env.local`、`.env.example`、`frontend/.env.production` 等）；已知二进制扩展；内容探测含 NUL 的构建文件；>2 MiB 文件。跳过数量写入 PASS 明细（本次 21 二进制 / 2 超大 / 2 `.env*`），不静默丢弃覆盖范围。
- 高置信规则（源文件与构建产物都适用）：PEM 私钥块、AWS Access Key、Google API Key、GitHub Token、Slack Token、OpenAI 风格 key、JWT。
- 硬编码凭据规则仍只作用于生产源码（`backend/internal` 非 `_test.go`/`testdata`、`frontend/src` 非测试文件、`executor/src`），避免把测试夹具中的 `fixture-pass` 误报为泄漏。
- 结果：0 发现。`.env` 未被跟踪且被 `.gitignore` 覆盖。
- 负向 fixture：向 `frontend/.next/static/chunks/secaudit-fixture.js` 写入形状匹配的假 token 后脚本必须 FAIL（文件名/规则名会打印，值不打印）；删除后恢复 PASS。已在 fix round 1 实测。

## 4. 权限矩阵

### 4.1 静态断言（每次运行）

| 端点组 | 权限 | 保护方式 |
|---|---|---|
| `GET /api/admin/audit` | admin | `identityhttp.RequireAdmin` |
| `GET/POST/PATCH /api/users`、`POST /api/users/{id}/reset-password` | admin | 5/5 路由 `RequireAdmin` |
| `/api/agents/*`（profiles/tools/sessions/messages） | admin | 12/12 路由 `guard()`；server 传入的 `adminGuard` 由 `RequireAdmin` 构造 |
| `/api/admin/llm/configs*` | admin | `guard()`；server 传入 `adminGuard` |
| `/api/admin/storage/objects*` | admin | `guard()`；server 传入 `adminGuard` |
| `/api/quality/eval-runs*` | admin | 3/3 路由 `wrap()`；server 传入 `qualityGuard`（`RequireAdmin`） |
| `/api/resources/*` 写/动作（含 db-target、takeover） | admin | 34/34 写/动作路由 `guard()`；server 传入的 `resourceGuard` 由 `RequireAdmin` 构造 |
| `POST /api/code/repos/{repoId}/index` | admin | `identityhttp.RequireAdmin` |
| `GET /api/stream`、`GET /api/stream/events` 的 `agent:*` channel | admin | `agentChannelAdminOnly` 校验 `RoleAdmin`，否则 403 |
| `GET /api/versions`、`/api/search`、`/api/todos`、`/api/agent-center/catalog`、`/api/issues`、`/api/reports`、`/api/resources/systems` | 登录 | 无 per-route guard；全局 Bearer 中间件覆盖除 `/api/health`、`/api/openapi.yaml`、`/api/auth/*` 外的所有 `/api` 路由 |

静态检查同时断言 `/api/admin|quality` 路由声明数与带 guard 数一致、`/api/resources/*` 的每个 POST/PUT/PATCH/DELETE 路由都带 `guard()`；未来新增的 admin-only 或资源写路由若未加 guard 会直接 FAIL。扫描前会剥离 Go 注释（`//` 与 `/* */`，字符串内的 `//`、`/*` 不会误伤），因此注释掉的路由/guard 不会污染声明数或造成假 PASS。通过 `RegisterRoutes/Register...` helper 注册的路由组，已知 helper 逐一核对 guard wiring；出现未知 helper 时记 SKIP 并列出名称，需人工复核，不会静默计入 PASS。

### 4.2 实时断言（本次 SKIP）

审计时 `http://127.0.0.1:13333` 未监听，脚本按约定记录：

```
[SKIP] live permission matrix: API is not reachable at http://127.0.0.1:13333; start it or run with -SkipLive
```

启动 API 后去掉 `-SkipLive` 会执行：

- 未认证 `GET /api/versions` → 401；
- 普通用户对 `GET /api/admin/audit`、`GET /api/users`、`GET /api/agents/profiles`、`GET /api/admin/llm/configs` → 403；
- 普通用户对 7 个只读端点 → 200；
- 若未提供 `-NormalUsername/-NormalPassword`，脚本创建一个临时 `secaudit*` 普通用户，运行后通过 `PATCH /api/users/{id}` 置为 `disabled`（P0 无删除用户路由，账户会保留为禁用态）。清理在 `finally` 中执行：`userId` 缺失或 PATCH 非 200/204 时记 **FAIL**（不会无条件 PASS），并提示手动禁用；两种 token 的 logout 同样在 `finally` 中执行，即使前面的断言提前返回也会登出。

## 5. 依赖许可清单

白名单以 MIT / Apache-2.0 为主，接受同属宽松许可的 ISC、BSD-2-Clause、BSD-3-Clause、0BSD、Unlicense、CC0-1.0、BlueOak-1.0.0。非白名单许可证会 FAIL，必须在 `scripts/security-audit.ps1` 的登记表中显式登记；新增依赖会自动进入清单，未登记且不在白名单的许可证会阻塞审计。

直接依赖 27 个（11 生产 + 16 开发）：

| 依赖 | 版本 | 范围 | 许可证 |
|---|---|---|---|
| `@radix-ui/react-dropdown-menu` | 2.2.1 | prod | MIT |
| `@radix-ui/react-tabs` | 1.2.1 | prod | MIT |
| `@xyflow/react` | 12.12.0 | prod | MIT |
| `clsx` | 2.1.1 | prod | MIT |
| `lucide-react` | 1.61.0 | prod | ISC |
| `next` | 16.4.3 | prod | MIT |
| `next-themes` | 0.4.6 | prod | MIT |
| `react` | 19.3.1 | prod | MIT |
| `react-dom` | 19.3.1 | prod | MIT |
| `tailwind-merge` | 3.7.0 | prod | MIT |
| `playwright` | 1.64.1 | prod | Apache-2.0 |
| `@biomejs/biome` | 2.6.3 | dev | MIT OR Apache-2.0 |
| `@playwright/test` | 1.64.1 | dev | Apache-2.0 |
| `@tailwindcss/postcss` | 4.3.3 | dev | MIT |
| `@testing-library/dom` | 10.4.2 | dev | MIT |
| `@testing-library/jest-dom` | 7.0.1 | dev | MIT |
| `@testing-library/react` | 16.3.3 | dev | MIT |
| `@types/node` | 24.19.2 | dev | MIT |
| `@types/react` | 19.3.0 | dev | MIT |
| `@types/react-dom` | 19.3.0 | dev | MIT |
| `axe-core` | 4.14.0 | dev | MPL-2.0（登记例外） |
| `jsdom` | 30.1.3 | dev | MIT |
| `tailwindcss` | 4.3.3 | dev | MIT |
| `typescript` | 5.9.3 | dev | Apache-2.0 |
| `vite` | 8.3.5 | dev | MIT |
| `vitest` | 5.0.5 | dev | MIT |
| `yaml` | 2.9.1 | dev | ISC |

例外说明：`axe-core` 为 MPL-2.0，仅用于测试期可访问性扫描，不进入 Next/executor 运行时产物；已在脚本中作为 dev-only 例外显式登记。登记同时校验 **许可证与 scope**：只要 `axe-core` 出现在生产依赖，即使许可证仍是 MPL-2.0 也会 FAIL（已用 fixture 验证）。P8 新增的 `@xyflow/react`（MIT）与 `axe-core`（MPL-2.0 dev-only）均有注册断言。

许可清单为空时的语义：如果一个已安装 scope 的 `node_modules` 都不存在，依赖清单与 P8 注册断言记 SKIP（无法得出许可证结论，但也不假装通过）；如果 `node_modules` 存在却解析不出任何依赖，则直接 FAIL。这样低覆盖和真实违规不会被混为一谈。

## 6. 已知边界

1. **token 仍在 sessionStorage**（P0 决策/D68）：无 HttpOnly Cookie 会话，XSS 仍可能窃取 token；CSP 目前为 report-only，尚不能作为缓解手段。
2. **TLS/反向代理由部署方负责**：仓库内 Next 安全头不覆盖 API 响应；生产需在反向代理/网关统一加 HSTS/CSP/头，并终止 TLS。
3. **D28 明文凭据**：`account_system_accesses.credential` 与 `sessions.storage_state` 为明文存储（用户决策）；D48 的数据库目标密码同样明文，但均限定 admin-only、API 不回显、不入日志/事件/证据/prompt。本审计不改变该决策。
4. **LLM `base_url` 由管理员配置且信任管理员**：本审计不做 SSRF/出站白名单。
5. **CSP 为 report-only**：当前不阻断内联脚本/样式；需在真实流量下收集报告后再切换为强制策略，迁移不仅是改 key：Next runtime 脚本需要 nonce/hash，内联样式需要 style nonce/hash，之后才能移除 `'unsafe-inline'`；生产 enforcing 前还应移除 `'unsafe-eval'`。
6. **`govulncheck` SKIP**：本机未安装该工具；Go 依赖漏洞未实测。安装 `golang.org/x/vuln/cmd/govulncheck@latest` 后重跑即可。
7. **`pnpm audit` 依赖 registry**：使用 `--json` 读取机器可读结果，并先判断漏洞证据再判断 registry/网络错误，避免同时包含两者的输出被降级为 SKIP；只有纯 registry 失败才记 SKIP，不视为漏洞通过。本次 registry 可用且 0 漏洞。
8. **实时权限矩阵本次未实测**：无运行中的 API；静态路由断言已通过。启动 API 后执行不带 `-SkipLive` 的命令可完成 403/200 实测。
9. **秘密扫描边界**：`.env*` 按 basename 在任意层级排除；本地工具链、运行时缓存（`.git/.tools/.tmp/.data/node_modules/.pnpm-store`、`.next/standalone|cache|dev`）不扫描；构建产物只扫描文本文件。二进制扩展、NUL 内容探测命中的文件、>2 MiB 文件会跳过并计数；不读取/打印 `.env` 值或发现的 secret 值。
10. **无 Docker**：未验证容器镜像内的头/权限配置；Compose/部署验证归 Task 33。
11. **共享工作区快照**：本文件的结果计数来自并行任务仍在修改的共享工作区；W3 gate 必须在完整分支上重跑 §1 命令并更新 §2 快照。`frontend/next.config.*` 出现多个文件时脚本按歧义 FAIL，需只保留实际生效的一份（本仓库为 `next.config.ts`）。

## 7. 后续动作

- 在 CI/验收机安装 `govulncheck` 并重跑，消除 Go 漏洞 SKIP。
- 启动 API 后执行一次不带 `-SkipLive` 的审计，保留 403/200 实测输出；并验证临时用户清理为 PASS（PATCH 失败/缺 id 时为 FAIL）。
- W3 gate 在完整分支重跑 `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1 -SkipLive` 与 `cd frontend; pnpm build`，更新本文件快照。
- 收集 CSP report-only 违规；迁移 nonce/hash（脚本/样式）并移除 `'unsafe-inline'`/`'unsafe-eval'` 后，才切换为强制 `Content-Security-Policy`。
- 部署侧补齐 TLS/HSTS/反向代理头；该边界不在 Next 配置内。
