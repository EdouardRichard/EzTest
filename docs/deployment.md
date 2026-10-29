# EzTest 部署指南（P8）

> 适用对象：在**内网或受控环境**部署 EzTest 的运维/平台人员。
> 配套文档：[运维手册](operations.md)、[开发指南](development.md)、[Compose 说明](../deploy/README.md)、[P8 发布说明](release-notes/p8.md)。
>
> **验证状态（必须阅读）**：本机没有安装 Docker/Compose，`deploy/` 下的镜像与 Compose **从未实际构建或启动**。本文与 `deploy/README.md` 中的命令是交付说明，只经过静态校验（`cd frontend; node scripts/validate-compose.mjs` 与 `scripts/acceptance.ps1 -StaticOnly` 的 Compose 检查）。首次在真实 Docker 主机执行前，请按本文“静态校验与未验证项”逐项复核。

## 1. 前置条件

| 项 | 要求 | 说明 |
|---|---|---|
| Docker Engine | 24+，Linux 容器模式 | Windows/macOS 需 Docker Desktop；本仓库未使用 Docker Compose v1。 |
| Docker Compose | v2（`docker compose`） | 命令不要用 `docker-compose`。 |
| 仓库 | 在构建上下文根检出 | Compose 的 `build.context` 为 `..`，需要 `backend/`、`frontend/`、`executor/`、`docs/`。 |
| 资源 | ≥4 vCPU / 8 GiB RAM / 20 GiB 磁盘起步 | executor 会运行 Chromium，`shm_size: 1gb`；真实执行与截图需要更多内存。 |
| 网络 | 能拉取 `postgres:17`、`golang:1.24.6-alpine`、`node:22-alpine`、`alpine:3.21` 与 pnpm 依赖 | 首次构建需要外网 registry。 |
| PostgreSQL | 17（Compose 内置） | 迁移使用 PostgreSQL advisory lock；不支持非 PostgreSQL 目标库（P9 候选）。 |
| 反向代理（建议） | Nginx/Caddy/企业网关 | 负责 TLS、HSTS、访问控制；仓库不内置。 |
| 密钥管理 | 独立密码库/密钥系统 | `.env` 与 `EZTEST_SECRET_KEYS` 不得进入备份或版本库。 |

浏览器侧要求：现代 Chromium/Firefox/Safari；前端使用 `sessionStorage` 保存 Bearer token（已知边界，见 [运维手册](operations.md)）。

## 2. `.env` 配置

从 `.env.example` 复制，**不要提交 `.env`**：

```powershell
Copy-Item .env.example .env
# 逐项替换 <placeholder>；不要打印或粘贴真实密钥
```

### 2.1 必填/关键键

| 键 | 用途 | 备注 |
|---|---|---|
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | Compose 的 PostgreSQL 与 API 连接串 | `POSTGRES_PASSWORD` 必填，无默认值。 |
| `EZTEST_DATABASE_URL` | 本地/非 Compose 运行的 API 连接串 | Compose 会覆盖为 `postgres://...@postgres:5432/...`；密码仍需与 `POSTGRES_PASSWORD` 一致。 |
| `EZTEST_SECRET_KEYS` | AES-256-GCM keyring，加密 LLM Key/headers | JSON `{"1":"<base64 32-byte key>"}`；**丢失则已加密凭据不可恢复**。 |
| `EZTEST_SECRET_KEY_ACTIVE` | 当前加密密钥版本 | 必须在 `EZTEST_SECRET_KEYS` 中存在；轮换见 [运维手册](operations.md#密钥与凭据轮换)。 |
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | 首次启动种子管理员 | 密码至少 10 字符；仅存 bcrypt 哈希。 |
| `EZTEST_EXECUTOR_TOKEN` | API ↔ executor 共享 Bearer token | Compose 拒绝缺失；只放在 `.env`。 |
| `EZTEST_WEB_ORIGIN` | API CORS 允许的浏览器 origin | 默认 `http://localhost:6868`；生产改为实际 HTTPS origin。 |
| `NEXT_PUBLIC_API_BASE_URL` | 浏览器访问 API 的 origin | **构建时**内联进前端；改值后必须重新构建 `web`。 |
| `EZTEST_OPENAPI_PATH` | OpenAPI 文档路径 | Compose 固定为 `docs/openapi/api.yaml`，镜像已内置。 |

### 2.2 数据目录（P8 Compose 已接线）

| 键 | 容器内路径 | 卷 | 内容 |
|---|---|---|---|
| `EZTEST_EVIDENCE_DIR` | `/data/evidence` | `evidence` | 执行证据文件（GC 只清这里）。 |
| `EZTEST_REPORT_DIR` | `/data/reports` | `reports` | 报告 Markdown；DB 只存相对路径/sha256/大小。 |
| `EZTEST_EXPORT_DIR` | `/data/exports` | `exports` | 脑图导出文件。 |
| `EZTEST_VERSION_ATTACHMENT_DIR` | `/data/version-attachments` | `attachments` | 需求附件。 |
| `EZTEST_STORAGE_DIR` | `/data/objects` | `objects` | 内容寻址对象存储。 |

### 2.3 P8 运行参数（按需）

| 键 | 默认 | 说明 |
|---|---|---|
| `EZTEST_RATE_LIMIT_RPS` / `BURST` / `SSE_MAX` | `50` / `100` / `10` | 每用户/IP 令牌桶与 SSE 并发；`RPS=0` 关闭普通桶。 |
| `EZTEST_GC_ENABLED` | `false` | 非 dry-run 证据清理开关；默认只允许 dry-run。 |
| `EZTEST_GC_RETENTION_DAYS` / `BATCH_KEEP` | `30` / `3` | 证据保留期与最近批保护。 |
| `EZTEST_INSTANCE_ID` | `hostname-pid` | 多副本部署必须每实例唯一。 |
| `EZTEST_LEADER_LOCK_KEY` | `1163411512` | 所有 API 实例必须一致；派生调度/自主/维护三个 key。 |
| `EZTEST_KNOWLEDGE_MAX_INJECT` / `MAX_BYTES` | `5` / `4096` | 版本知识注入条数与字节预算；`0` 关闭对应预算。 |
| `EZTEST_VISUAL_BASELINE_MAX_BYTES` | `5242880` | 视觉基线大小上限；API 配置与 executor 都读取，API 范围 64 KiB–20 MiB；审批还校验 PNG 尺寸=viewport×DPR。 |
| `EZTEST_VISUAL_DIFF_THRESHOLD` | `0.005` | **预留**：当前未读取；阈值来自每条 visual 断言的 `threshold`（0–0.1）。 |
| `EZTEST_AGENT_FILE_ROOT` | 空 | 为空时 Agent 文件工具不注册；需要时挂载只读目录并指向容器内路径。 |
| `EZTEST_AGENT_HTTP_ALLOWLIST` | `127.0.0.1,localhost` | Agent `http.get` 白名单；生产按需最小化。 |
| `EZTEST_AUTONOMY_BATCHES_PER_TICK` | `4` | 自主 worker 每 tick 批预算（1–20）；非整数/越界启动失败，不是回退 4。 |

> 注意：设计规格中的 `EZTEST_CHAIN_MAX_NODES`、`EZTEST_SEARCH_MAX_RESULTS_PER_TYPE`、`EZTEST_TODOS_MAX` 当前由代码内置默认/接口 `limit` 约束，**构建尚未读取这些环境变量**。视觉断言已接线：API 与宿主启动的 executor 都读取 `EZTEST_VISUAL_BASELINE_MAX_BYTES`（默认 5 MiB），Compose 的 executor 服务当前未注入该变量、按默认 5 MiB 运行；`EZTEST_VISUAL_DIFF_THRESHOLD` 仍未读取。真实 Chromium/executor 端到端验收归 Task 35。不要在生产依赖未接线的变量，详见 [P8 发布说明](release-notes/p8.md)。
>
> `.env.example` 已包含限流/GC/leader/知识与视觉基线变量（`EZTEST_VISUAL_DIFF_THRESHOLD` 标注为“预留”）；未接线的链路上限/检索/待办变量请按需手工补充。

### 2.4 可选首次启动种子

- `llm_base_url` / `llm_key` / `llm_model_name`：小写是历史约定；`llm_key` 留空则不做真实 LLM 调用，登录后在设置页配置。
- `EZTEST_LOG_LEVEL`：`debug` / `info` / `warn` / `error`。
- `EZTEST_ENABLE_TEST_ENDPOINTS`：默认 `false`；仅本地/测试开启。
- `EZTEST_SNOWFLAKE_NODE_ID`：0–1023；多 API 实例必须各不相同。

## 3. Compose 启动

在仓库根执行：

```powershell
# 1) 构建并启动（首次会拉取镜像）
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build

# 2) 查看状态与健康检查
docker compose --env-file .env -f deploy/docker-compose.yml ps

# 3) 跟踪 API 日志（迁移、种子、锁、限流命中）
docker compose --env-file .env -f deploy/docker-compose.yml logs -f api

# 4) 停止但保留数据
docker compose --env-file .env -f deploy/docker-compose.yml down

# 5) 连数据卷一起删除（不可恢复；只用于一次性测试环境）
docker compose --env-file .env -f deploy/docker-compose.yml down -v
```

启动顺序：`postgres` 健康 → `api` 健康 → `web`；`executor` 与 `api` 并行启动，但 API 只有执行批次时才访问 executor。API 启动时自动执行嵌入迁移、种子管理员/默认 LLM/profile，并在 advisory lock 下保证重复启动幂等。

访问地址（默认端口）：

| 入口 | 地址 |
|---|---|
| Web | <http://localhost:6868> |
| API 存活探针 | <http://localhost:13333/healthz> |
| API 就绪探针 | <http://localhost:13333/api/health> |
| OpenAPI | <http://localhost:13333/api/openapi.yaml> |
| PostgreSQL | `localhost:5433`（仅建议本机/受控网络） |

## 4. 持久化卷与代码仓挂载

| 卷 | 容器路径 | 是否必须持久化 |
|---|---|---|
| `pgdata` | `/var/lib/postgresql/data` | 是（业务库） |
| `objects` | `/data/objects` | 是（内容寻址对象） |
| `attachments` | `/data/version-attachments` | 是（需求附件） |
| `evidence` | `/data/evidence` | 是（执行证据；GC 仅清此目录） |
| `reports` | `/data/reports` | 是（报告 Markdown） |
| `exports` | `/data/exports` | 是（脑图导出） |

`Dockerfile.api` 已创建这些目录并 chown 给运行时用户；Compose 的命名卷挂载后仍由该用户持有。备份时必须同时覆盖 PostgreSQL 与上述五个内容目录，详见 [运维手册](operations.md#备份与恢复)。

### 代码仓只读挂载

P4 的代码仓来自 P2 资源中登记的 `localPath`；容器内需要能只读访问该路径。`docker-compose.yml` 预留了注释示例：

```yaml
# api.volumes:
#   - ${EZTEST_CODE_REPO_MOUNT:-./repos}:/repos:ro
```

启用步骤：

1. 在宿主准备代码仓目录（可多仓，如 `/srv/repos/app`）。
2. 在 Compose 中取消注释并改成实际路径，挂载点为 `/repos`（只读）。
3. 重新 `up -d api`。
4. 管理员在 `/code-repos` 新建仓库时把 `localPath` 填成容器内路径（例如 `/repos/app`）。
5. 索引/分析只读 Git 工作树；不要给写权限，也不要把仓库放在 `/data` 持久卷里。

如还需要 Agent `file.list`/`file.read`，把同一只读挂载指给 `EZTEST_AGENT_FILE_ROOT=/repos`（或更小的子目录），并保持只读。

## 5. 反向代理、TLS 与安全头

生产不要直接暴露 13333/6868，建议由同一域名反向代理：

```nginx
# 示例：/ -> web:6868，/api/ -> api:13333；按实际域名/证书替换
server {
  listen 443 ssl http2;
  server_name eztest.example.internal;

  ssl_certificate     /etc/ssl/certs/eztest.crt;
  ssl_certificate_key /etc/ssl/private/eztest.key;

  add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
  add_header X-Content-Type-Options "nosniff" always;
  add_header Referrer-Policy "strict-origin-when-cross-origin" always;
  add_header X-Frame-Options "DENY" always;

  client_max_body_size 12m;

  location /api/ {
    proxy_pass http://api:13333;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    # SSE：关闭缓冲并放宽读超时
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
  }

  location / {
    proxy_pass http://web:6868;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
  }
}
```

要点：

- **TLS 终止**：仓库不提供 TLS；由代理/网关负责。启用 HTTPS 后把 `EZTEST_WEB_ORIGIN` 和 `NEXT_PUBLIC_API_BASE_URL` 改为 `https://...` 并重建 `web`（后者是构建时变量）。
- **SSE**：代理必须关闭响应缓冲、允许长连接，否则探索链路/执行会话会表现为“一直重连/没有消息”。`proxy_read_timeout` 至少大于 20s 心跳间隔。
- **安全头**：Next 已下发 `X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options`、`Permissions-Policy` 与 **report-only** CSP；API 响应本身不带这些头，需代理补齐。CSP 当前不强制（Next 运行时/Tailwind 有内联），不要直接改成 enforcing，详见 `docs/acceptance/p8/security-audit.md`。
- **HSTS** 只在确认全站 HTTPS 后开启。
- 只暴露 `web`（和代理到 `api` 所需的内部网络）；`postgres` 的 5433、executor 的 13336 不应暴露到公网/办公网。executor 无宿主端口，只有 API 持有 token。
- CORS 只允许 `EZTEST_WEB_ORIGIN`；同域反代时也要保证浏览器看到的 origin 与配置一致。
- 管理员入口没有额外 IP 白名单；如需要，在代理层限制 `/settings`、`/api/admin/*`、`/api/quality/*` 等路径。

## 6. 备份前置

在首次备份前确认：

1. 五个内容目录已创建，且 API 至少成功启动过一次（`backup.ps1` 默认在目录缺失时失败，防止备错目录）。
2. 选择低写入窗口；备份期间仍可能有新证据/报告写入，manifest 的行数稳定标记只作 advisory。
3. 按 [运维手册](operations.md#备份与恢复) 执行 `scripts/backup.ps1`，并把 `manifest.json` 与数据库 dump、目录 tar 一起保管。
4. **不要把 `EZTEST_SECRET_KEYS` 放进备份**；它必须独立保存。丢失 keyring 时，备份中的 LLM Key/headers 只有密文，无法恢复。
5. 备份未加密（tar + sha256）；生产需用加密卷/对象存储服务端加密/访问控制保护备份目录。
6. 至少演练一次“空库/空目录恢复 → `scripts/restore.ps1` → 校验 → 应用 smoke”。P8 已记录本机 RPO/RTO 与 175/175 断言，但未在恢复库上做应用级 smoke（defer Task 35），见 `docs/acceptance/p8/backup-drill.md`。

## 7. 健康检查与观测

| 探针 | 行为 | 用途 |
|---|---|---|
| `GET /healthz` | 只要进程能响应就 200 `{"status":"ok"}` | 存活探针；Compose 的 API healthcheck 使用它。 |
| `GET /api/health` | 2s 内 ping DB；DB 失败 503 `status=unavailable/db=down`；返回版本/commit、`llm_configured`、生效的 `rate_limit` 与 `gc` 参数 | 就绪探针/排障；不返回路径或密钥。 |
| `GET /api/openapi.yaml` | 返回内置 OpenAPI；缺失文件 404 | 工具发现。 |
| executor `GET /healthz` | 返回 executor 状态/版本/上下文容量 | Compose 内部 healthcheck。 |
| web `GET /login` | 200 | Compose 的 web healthcheck。 |

观测建议：

- API 日志走 stdout/stderr（`EZTEST_LOG_LEVEL` 控制级别），带 `request_id`；用 `docker compose logs -f api` 或日志驱动采集。
- 限流命中日志包含 path/scope/reason/limit/retry_after，不含 token/用户标识明文。
- 健康检查、静态资源、OpenAPI 不计入普通令牌桶；SSE 单独按并发计数。
- 每日证据维护默认 dry-run；日志只有计数/阈值，不含证据路径或内容。
- 监控 PG 连接数、磁盘（证据增长最快）、executor 并发与 429/5xx。

## 8. 升级与迁移

1. **先备份**（数据库 + 五个内容目录 + manifest），并确认备份成功。
2. 拉取/检出目标 commit，检查 `backend/internal/db/migrations/order.txt` 与新增迁移。
3. 重建并滚动启动：
   ```powershell
   docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
   docker compose --env-file .env -f deploy/docker-compose.yml ps
   ```
4. API 启动时在 advisory lock 下按 `order.txt` 顺序执行未应用迁移；`schema_migrations` 记录版本与校验和，重复启动幂等。
5. 迁移只追加、不回滚：不要修改已应用的历史 SQL；如需回退版本，使用升级前备份恢复到新库/新目录，而不是执行 down migration。
6. 升级后检查 `/api/health`（DB、版本/commit、限流/GC 参数）、`/api/openapi.yaml`、关键页面 smoke 与日志错误。
7. **报告指纹一次性 stale**：P8 首次升级后，旧发布报告在第一次读取时会因 `input_fingerprint` 口径变化（纳入证据/失败分类计数）被标记 stale 一次；旧 revision 内容与 Markdown 不重写，重新生成会追加 revision。见 [P8 发布说明](release-notes/p8.md)。
8. 多副本部署升级时逐实例滚动；所有实例共享同一 PostgreSQL 与 `EZTEST_LEADER_LOCK_KEY`，每个实例 `EZTEST_INSTANCE_ID` 唯一。

## 9. 常见故障

| 现象 | 可能原因 | 处理 |
|---|---|---|
| `api` 反复重启，日志 `database`/`ping` 失败 | PG 未健康、密码不一致、`EZTEST_DATABASE_URL` 被 Compose 覆盖后仍指向错误库 | `docker compose ps`、`logs postgres`；核对 `POSTGRES_*` 与 `EZTEST_DATABASE_URL`；等待 `service_healthy`。 |
| 启动失败，迁移校验和不匹配 | 修改了已应用的迁移文件 | 不要改历史迁移；从备份恢复或新增迁移修正。 |
| 登录后页面一直 401 | token 过期/被撤销；`EZTEST_SECRET_KEYS` 或时钟异常；`EZTEST_WEB_ORIGIN` 不匹配 | 重新登录；检查系统时间、keyring、CORS origin 与浏览器地址是否一致。 |
| 429 `rate_limited` | 超过 `EZTEST_RATE_LIMIT_RPS/BURST` 或 SSE 并发 | 看 `Retry-After`；调整阈值或减少并发；限流按实例内存计数，多副本需按实例预算合计。 |
| SSE 一直“重连中”/无实时消息 | 反向代理缓冲、超时、HTTP/1.0 | 关闭 `proxy_buffering`，`proxy_read_timeout` 放宽，使用 HTTP/1.1；确认 `/api/stream` 未被网关拦截。 |
| `POST /api/admin/gc` 返回 409 | `EZTEST_GC_ENABLED=false` 时请求了 `dryRun:false` | 先 dry-run；确实要清理时显式设为 `true` 并重启 API。 |
| GC 候选大量“跳过” | 文件缺失/哈希不符/新引用 | 预期保护行为；核对 `purged_at` 与磁盘，不自动删除这些行。 |
| 报告显示 stale，但内容没变 | 升级后指纹口径变化（一次性）或证据计数变化 | 确认是否首次升级；必要时“重新生成”追加 revision；不要手改 DB。 |
| 批次无法创建/执行，executor 不健康 | executor 容器崩溃、token 不一致、Chromium 内存不足 | `logs executor`；核对 `EZTEST_EXECUTOR_TOKEN`；增加内存/`shm_size`。 |
| LLM 调用 503 `llm_not_configured` | 未配置默认 LLM 或 key 解密失败 | 管理员设置页重新配置；检查 keyring 版本；`/api/health` 的 `llm_configured`。 |
| 管理员接口 403 | 普通用户 token 或管理员被禁用 | 用管理员账号；检查用户角色/状态与审计日志。 |
| 磁盘增长快 | 证据/报告/附件/导出未清理 | 备份后按保留策略 dry-run GC（仅证据自动清理；其他对象 P8 不自动删除）。 |
| web 构建后 API 地址仍旧 | `NEXT_PUBLIC_API_BASE_URL` 是构建时变量 | 修改后必须重建 `web` 镜像，而不是只重启容器。 |

## 10. 静态校验与未验证项

本机可做：

```powershell
# Compose 字段/卷/健康检查/依赖顺序/Dockerfile（不需要 Docker）
cd frontend
node scripts/validate-compose.mjs

# P8 文档/OpenAPI/Compose 静态契约（四类文档、关键 path、卷、环境变量键、无秘密）
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/check-docs.ps1

# 全量静态验收（含上一行与 P1–P8 契约；不需要数据库/Playwright）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly
```

**未验证项（诚实标注）**：

- 镜像构建、Compose 启动、容器网络、卷权限、健康检查在真实 Docker 上均未实跑。
- 未验证反代/TLS/安全头在真实网关上的行为；示例 Nginx 片段不是已测试配置。
- 未验证多副本下的真实 leader 抢占、限流合计与滚动升级；P8 只做了双连接/双实例单测。
- 未验证大容量备份恢复的 RTO/RPO；本机演练规模很小，且未做恢复库应用 smoke（defer Task 35）。

## 11. 相关文档

- [运维手册](operations.md)：备份/恢复、GC、leader、限流/SLO、审计、密钥轮换、故障处理。
- [用户手册](user-guide.md)：版本/需求/脑图/执行/问题/报告/搜索/待办/知识/质量中心。
- [开发指南](development.md)：架构、测试、E2E、评测、迁移、依赖治理。
- [Compose 说明](../deploy/README.md)：文件布局、环境变量、静态校验。
- [P8 发布说明](release-notes/p8.md)：行为变化、已知限制、升级注意。
- [P8 安全审计](../docs/acceptance/p8/security-audit.md)：安全头、权限矩阵、依赖许可。
