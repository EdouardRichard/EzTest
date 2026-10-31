# EzTest 0.2 执行器协议契约（P3）

> 状态：`frozen: P3`（2026-11-01；创建即冻结，P4+ 只允许增量）。
> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p3-execution-design.md` §6.1/§6.2/§6.10；`architecture-design.md` §11；`high-level-design.md` §1.3。
> 实现：`executor/src`（Node + Playwright）与 `backend/internal/execution/infrastructure/executor`（API 侧客户端）。
> 边界：执行器只执行动作/回传证据，不接触 LLM 上下文、不实现 case/wave 语义（P3-D-10/P3-D-35）。P3 的执行主体是 API 内 Case Executor Agent。

## 1. 进程与配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `EZTEST_EXECUTOR_ADDR` | `127.0.0.1:13336` | 监听地址；容器覆盖为 `0.0.0.0:13336`（loopback 优先） |
| `EZTEST_EXECUTOR_TOKEN` | 必填 | 所有 `/v1` 端点的 Bearer token；**执行器进程自身 env**，不是 EzTest API 启动 env |
| `EZTEST_EXECUTOR_MAX_CONTEXTS` | `8` | 并发 browser context 上限（= healthz `maxContexts` = `executor_instance.capacity`） |
| `EZTEST_EXECUTOR_CHROMIUM_PATH` | 未设置 | 可选显式 Chromium 二进制（本地开发） |
| `EZTEST_EXECUTOR_ALLOW_DATA_PAGES` | 未设置 | `1` 仅允许 `data:text/html` 自测页 |

- EzTest API 仍只有严格 4 项启动 env；API 通过 DB 设置读取 `execution.executor_url` 与 `execution.executor_token`（`is_secret=true`，P3 首个 secret 键）。`scripts/dev-executor.ps1` 从 DB 读取当前 token 并注入执行器进程的 `EZTEST_EXECUTOR_TOKEN`。
- 协议版本：HTTP 路径前缀 `/v1`；`GET /healthz` 的 `version` 返回执行器包版本（当前 `0.1.0`），API 记录到 `executor_instance.version`。P3 不改变既有 action 语义，仅新增 context `metadata` 与 `GET /v1/contexts`（P3-D-35）。

## 2. 认证与安全

- 除 `GET /healthz` 外，所有端点要求 `Authorization: Bearer <token>`；token 只从请求头读取，比较使用常量时间，**永不写日志/事件/证据**。
- API 侧只存 `executor_instance.token_hash = sha256(token)`（64 hex），不存明文；P3 单 endpoint。
- 执行器绑定 loopback（默认）；每个 context 的 `allowedHosts` 白名单限制页面/子资源/重定向 host（`*.suffix`/`.suffix` 只匹配子域，apex 需显式列出）；`file:`/`javascript:` 等 scheme 拒绝；service worker 阻断；`fill(secret=true)` 的值从 console/network/snapshot/错误中擦除（每 context 最多 32 条、每条 ≤1024 字符，超限拒绝而不是静默淘汰）。
- 执行器不接触 LLM prompt/上下文，不持久化证据字节；artifact 只随 action 响应回传，由 API 写入内容账本。

## 3. HTTP 协议

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `GET /healthz` | — | `{status:"ok"\|"degraded",version,contexts,maxContexts,browserVersion?,error?}`（200/503；无 auth） |
| POST | `POST /v1/contexts` | `{key,allowedHosts,baseUrl,storageState?,sessionStorage?,viewport?,metadata?}` | `{contextId,browserVersion}`（200） |
| POST | `POST /v1/contexts/{id}/actions` | `{action,args}` | `{ok:true,result,artifacts}` 或 `{ok:false,error:{code,message,details?}}`（200/400/404/413） |
| POST | `POST /v1/contexts/{id}/cancel` | — | `{ok:true,result:{canceled}}`（200） |
| DELETE | `DELETE /v1/contexts/{id}` | — | `{ok:true,result:{closed:true}}`（200；404 `context_not_found` 视为已关闭，best-effort） |
| GET | `GET /v1/contexts` | — | `{contexts:[{id,key,metadata,createdAt}]}`（**P3 扩展**；Bearer） |

### 3.1 `POST /v1/contexts`（P3 扩展 `metadata`）

- 必填/可选：`key`（string，P3 形如 `run:{case_run_id}:vs:{version_sut_id}:role:{role}`）、`allowedHosts`（非空 host 字符串数组）、`baseUrl`（该 version_sut 环境）；可选 `storageState`、`sessionStorage`、`viewport`。
- **P3 新增 `metadata`**：JSON object，≤2KB；API 写入 `{case_run_id,version_sut_id,account_role,dispatch_epoch,dispatch_token}`；执行器保存并只在 `GET /v1/contexts` 返回。非 object 或超限 → 400 `invalid_arguments`。
- 超过 `maxContexts` → 409 `context_limit`。

### 3.2 `GET /v1/contexts`（P3 扩展）

- 返回当前所有 context 的 `id/key/metadata/createdAt`；用于容量核对与孤儿 context 清理（API 每 60s 对账，只删除 key 前缀 `run:` 且 DB 无记录的 context）。
- 不返回 cookie/storageState/页面内容等敏感数据。

### 3.3 Actions

`action ∈ goto, snapshot, click, fill, press, wait_for, screenshot, console, network, download, storage_state, close`。

- `args` 按 action 定义；Target 显式四选一：`{role,name,exact}`、`{text}`、`{test_id}`、`{selector}`。
- 零匹配 → `target_not_found`；单选动作多匹配 → `target_ambiguous`。
- `goto` 仅允许 http(s)，host 必须在 context `allowedHosts` 或 `baseUrl` host 内，否则 `host_not_allowed`。
- artifact：`{kind,mime,base64,sha256?,bytes,filename?}`；截图/下载/快照/console/network 等由 API 映射为 evidence kind。
- 取消：`cancel` 关闭并重建当前 page，确保浏览器侧导航/等待真正终止；下一 action 从干净 page 开始。

## 4. 限额（P3 冻结）

| 项 | 限额 |
|---|---|
| 请求体 | ≤1MB（普通路由）；action 路由在 P8 视觉基线场景可接受更大请求体，不属于 P3 契约 |
| 单 action 超时 | 15s；`goto` 30s；最大 60s（`timeout_ms` 钳制） |
| artifact | ≤10MB（下载先 stat 再读；截图先 buffer 后校验） |
| snapshot | ≤12KB（超出截断并返回 `truncated:true`） |
| maxContexts | 执行器配置，默认 8 |
| ring buffer | console/network 各 500 条 |

超限错误：`too_large`（413）。

## 5. 错误码

```json
{"ok":false,"error":{"code":"...","message":"...","details":{...}}}
```

| code | HTTP | 触发 |
|---|---|---|
| `unauthorized` | 401 | 缺失/错误 Bearer token |
| `invalid_request` | 400 | JSON 非法、body 非 object、方法不支持 |
| `invalid_arguments` | 400 | 参数/action/Target/viewport/storageState/metadata 非法 |
| `context_not_found` | 404 | context 不存在/已关闭 |
| `context_limit` | 409 | 超过 maxContexts |
| `too_large` | 413 | 请求体/artifact 超限 |
| `target_not_found` / `target_ambiguous` | 200（action 失败） | Target 解析失败 |
| `host_not_allowed` | 200（action 失败） | host 不在白名单 |
| `internal` | 500 | 执行器内部错误 |

- action 失败仍返回 HTTP 200 与 `ok:false`（除非参数/大小/context 层错误用 4xx）；API 将错误映射为 case/step 的 `environment`/`automation` failure_class。
- 执行器重启导致 context 丢失：browser 工具返回 `context_not_found` → API 将 case_run 置 `interrupted(environment)` 并按 §5.5 重派。

## 6. 多 SUT 上下文映射

- 每个 `(version_sut_id, account_role)` 一个 context；同一 case_run 的步骤**串行**执行，context 复用。
- `key = "run:{case_run_id}:vs:{version_sut_id}:role:{role}"`（role 空用 `""`）。
- `allowedHosts = [该 version_sut 环境 base_url 的 host]`（demo 可追加 `127.0.0.1`）；`baseUrl = environment.base_url`。
- `metadata = {case_run_id, version_sut_id, account_role, dispatch_epoch, dispatch_token}`（≤2KB；token 不得用于业务鉴权，只用于 API 对账/防串）。
- 跨系统用例的每个 `(version_sut, role)` 各建 context；结果/证据/断言均带 `version_sut_id`；页面/API/账号/DB 必须属于同一 version_sut。
- 生命周期：懒创建（首个 browser 工具）；`case_run_sut_context.browser_context_id` 记录；终态/取消/immediate 暂停 → `DELETE` 全部 context（404 忽略）；非 running case 的 context 超过 `execution.executor_context_ttl_seconds` 清理；running 中的 context 不按 TTL 清理（避免打断执行）。
- P3 不实现远程浏览器/VNC/可视化接管；登录由 Agent 按步骤执行，2FA/验证码走 `human.request`（暂停-恢复 + 一次性响应）。

## 7. API pull 注册 / 心跳 / 容量 / 排空 / offline

P3 采用 **API pull healthz**（P3-D-01），执行器不主动 push：

1. API 读取 `execution.executor_url` 与 `execution.executor_token`；每 `execution.executor_heartbeat_seconds`（默认 10s，1–60）调用 `GET {execution.executor_url}/healthz`。
2. 成功：upsert `executor_instance`（`endpoint` 唯一；`name=host:port`；`capacity=maxContexts`；`active_contexts=contexts`；`version=version`；`last_heartbeat=now`；`last_error=''`；`token_hash=sha256(token)`）。状态：`draining` 保持，其余 → `active`。healthz `503 degraded` 仍更新 `last_heartbeat`，状态保持 `active`（degraded 只影响告警/派发前复检）。
3. 失败：已有行且 `now - last_heartbeat > execution.executor_offline_seconds`（默认 30s，5–300）→ `offline` 并记录 `last_error`；无行 → 不建（未注册）。
4. 管理员：`GET /api/admin/executors`、`POST /api/admin/executors/{id}/drain|activate`；`draining` 只停止新派发，在飞 case_run 继续；`activate` 允许 `draining/offline → active`。
5. 调度只选当前 endpoint 的 active 行；`offline` 时 pending 等待恢复，running 由 reconciler 置 `interrupted(executor_lost)` 并按 `dispatch_epoch++` 重派（最多 3 次）。
6. P3 **不实现** `/api/internal/executors/*` push 端点与内部鉴权 scheme；多副本注册/心跳 fan-out 留 P7。

## 8. Artifact → 内容账本 / 证据

- 执行器 artifact base64 回传；API 解码后**重算 sha256**（不信任执行器自报）、校验 ≤ `execution.evidence_max_bytes`（5 MiB，P3-D-21）→ `storage.Upload`（P0 `content_object`）→ `storage.AddRef(owner_kind='evidence', owner_id=evidenceID, role=kind)` → 插入 `evidence` 行（先分配雪花 ID、Upload、AddRef、再插行）。
- evidence kind 映射：screenshot→`screenshot`；snapshot→`snapshot`；console→`console`；network→`network`；http.request→`http`；db.query→`db`；download→`download`；text→`text`。
- 每 `case_run` ≤100 条，超出丢弃并记 `summary.evidence_skipped`（不失败）；超限返回 `evidence_too_large`，用例继续。
- `evidence` 只存 `content_ref_id/sha256/size_bytes/mime/filename/kind/version_sut_id/...`；**不写 `paths.evidence_dir`**（P3-D-20 例外）；API 不返回 content_ref/路径；下载走 `GET /api/versions/{id}/evidence/{evidenceId}/content` 并校验 sha/size/归属。
- 失败自动取证：case_run 进入 failed/blocked/inconclusive 时，若仍有活跃 context，best-effort 截图一次。

## 9. 等待与时间窗口（`execution.wait`，P3-D-41）

- Agent 工具 `execution.wait {seconds:int(1..300),reason?:string(0..200)}` 在 **API 进程内**等待（timer/ctx select），**不调用执行器**，不占用执行器 action 60s 上限；页面 JS 定时器在 headless 下继续运行。
- 等待期间 runner 心跳继续续 `case_run.lease_until`；返回 `{waited_seconds,deadline_at}`，不产生证据（需要时等待后显式 `browser.snapshot`/`browser.screenshot`）。
- 可被暂停/取消/lease 丢失/执行器 offline 打断（`lease_lost`/`state_conflict`）；case_run 按暂停/中断状态机处理，恢复后该 step 重试（等待重跑）。
- 单次 ≤300s；累计等待计入 `execution.case_timeout_seconds`（默认 900s，可 admin 调整）；P3 不承诺 >900s 的无限等待。
- **执行器进程不实现 sleep**；`browser.wait_for` 仍只用于 ≤60s 的可见性等待。120s 时间窗口纳入 `-LongWait` 验收（默认 10s 窗口）。

## 10. P7 保留与变更控制

- P7 保留（P3 不实现）：`/api/internal/executors/*` push 注册/心跳、内部鉴权 scheme、多副本执行器调度与 fan-out、跨实例实时、远程浏览器/VNC、`storage_state` 导入/复用、条件轮询工具（如 wait_until）、>300s 单次等待。
- 执行器协议 P3 冻结后只允许**增量**：新增 action/字段必须同时更新本文件、`executor/src`、API 客户端与测试；破坏性变更（改既有 action 语义/认证/大小限额）需先更新契约/决策/handoff。
- token/注册方式（pull）与 HLD 的 `/api/internal/executors/*` push 差异记录为 P3-D-01/P3-D-02，P7 多副本时补 push（记录于 §10 与 handoff）。
