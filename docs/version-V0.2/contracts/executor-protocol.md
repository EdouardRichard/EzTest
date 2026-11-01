# EzTest 0.2 执行器协议契约（P3）

> 状态：`frozen: P3`（2026-11-01；创建即冻结，P4+ 只允许增量）。
> P5 增量：§11（`visual.compare`，协议 v1.2，`frozen: P5`，2026-11-01）；§3.3/§4/§8 只做增量补充，P3 语义不变。
> P8 增量：§12.3 多端点 per-executor 派发转正为 P8 已实现（`frozen: P8`，2026-11-02）；线协议仍 v1.2，action/认证/限额不变。
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
| `EZTEST_API_URL` | 未设置 | P7 可选：EzTest API 基址；设置后启用 push 注册/心跳，空=仅 pull |
| `EZTEST_EXECUTOR_HEARTBEAT_SECONDS` | `10` | P7 可选：push 心跳间隔（整数 1–60s）；仅 push 启用时使用 |

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

`action ∈ goto, snapshot, click, fill, press, wait_for, screenshot, console, network, download, storage_state, close, visual.compare`（`visual.compare` 为 P5 增量，见 §11）。

- `args` 按 action 定义；Target 显式四选一：`{role,name,exact}`、`{text}`、`{test_id}`、`{selector}`。
- 零匹配 → `target_not_found`；单选动作多匹配 → `target_ambiguous`。
- `goto` 仅允许 http(s)，host 必须在 context `allowedHosts` 或 `baseUrl` host 内，否则 `host_not_allowed`。
- artifact：`{kind,mime,base64,sha256?,bytes,filename?}`；截图/下载/快照/console/network 等由 API 映射为 evidence kind。
- 取消：`cancel` 关闭并重建当前 page，确保浏览器侧导航/等待真正终止；下一 action 从干净 page 开始。

## 4. 限额（P3 冻结）

| 项 | 限额 |
|---|---|
| 请求体 | ≤1MB（普通路由与其余 action）；`visual.compare` ≤ `visual.baseline_max_bytes`（DB 设置，默认 5 MiB，64 KiB..20 MiB；P5 v1.2） |
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
- evidence kind 映射：screenshot→`screenshot`；snapshot→`snapshot`；console→`console`；network→`network`；http.request→`http`；db.query→`db`；download→`download`；text→`text`；**P5** `visual.compare` 的 actual→`visual_actual`、diff→`visual_diff`（`screenshot` 仍为通用截图）。
- 每 `case_run` ≤100 条，超出丢弃并记 `summary.evidence_skipped`（不失败）；超限返回 `evidence_too_large`，用例继续。
- `evidence` 只存 `content_ref_id/sha256/size_bytes/mime/filename/kind/version_sut_id/...`；**不写 `paths.evidence_dir`**（P3-D-20 例外）；API 不返回 content_ref/路径；下载走 `GET /api/versions/{id}/evidence/{evidenceId}/content` 并校验 sha/size/归属。
- 失败自动取证：case_run 进入 failed/blocked/inconclusive 时，若仍有活跃 context，best-effort 截图一次。

## 9. 等待与时间窗口（`execution.wait`，P3-D-41）

- Agent 工具 `execution.wait {seconds:int(1..300),reason?:string(0..200)}` 在 **API 进程内**等待（timer/ctx select），**不调用执行器**，不占用执行器 action 60s 上限；页面 JS 定时器在 headless 下继续运行。
- 等待期间 runner 心跳继续续 `case_run.lease_until`；返回 `{waited_seconds,deadline_at}`，不产生证据（需要时等待后显式 `browser.snapshot`/`browser.screenshot`）。
- 可被暂停/取消/lease 丢失/执行器 offline 打断（`lease_lost`/`state_conflict`）；case_run 按暂停/中断状态机处理，恢复后该 step 重试（等待重跑）。
- 单次 ≤300s；累计等待计入 `execution.case_timeout_seconds`（默认 900s，可 admin 调整）；P3 不承诺 >900s 的无限等待。
- **执行器进程不实现 sleep**；`browser.wait_for` 仍只用于 ≤60s 的可见性等待。120s 时间窗口纳入 `-LongWait` 验收（默认 10s 窗口）。

## 10. P3 之后保留与变更控制

- P3 记录为 P7 候选项且 **P7 已实现**：`/api/internal/executors/*` push 注册/心跳、内部鉴权 scheme、多端点 poll/容量选择、跨实例实时（详见 §12）；**P8 实现** §12.3 多端点 per-executor 派发（线协议仍 v1.2）。
- 仍保留（不实现）：远程浏览器/VNC、`storage_state` 导入/复用、条件轮询工具（如 wait_until）、>300s 单次等待。
- 执行器协议 P3 冻结后只允许**增量**：新增 action/字段必须同时更新本文件、`executor/src`、API 客户端与测试；破坏性变更（改既有 action 语义/认证/大小限额）需先更新契约/决策/handoff。
- token/注册方式（pull）与 HLD 的 `/api/internal/executors/*` push 差异记录为 P3-D-01/P3-D-02；P7 已补 push（§12），P8 仅把多端点派发转正（§12.3），线协议仍 v1.2。

## 11. P5 增量：`visual.compare`（协议 v1.2，`frozen: P5`，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p5-codeintel-visual-design.md` §4.6/§7.3/§7.4（P5-D-08）。
> 实现：`executor/src`（Playwright + pixelmatch）与 `backend/internal/execution/infrastructure/executor`（API 侧客户端）；P3 冻结的其它 action/认证/限额语义不变。

### 11.1 Action `visual.compare`

| 项 | 内容 |
|---|---|
| action | `visual.compare`（`POST /v1/contexts/{id}/actions`，与既有 action 同一路由） |
| 请求 | `{baseline?:{base64,sha256?,bytes}\|null, selector, region?, threshold, viewport:{width,height}, device_scale_factor, wait_policy?}` |
| 响应 artifact | `actual_png`（必需）+ `diff_png`（有基线且环境匹配时） |
| 响应 result | `{diff_ratio,diff_pixels,total_pixels,width,height,environment_match,environment:{viewport_width,viewport_height,device_scale_factor,browser,version},mismatch_boxes}` |

- `baseline` 为 `null`/缺省表示 **capture-only**（无基线首跑）：只回 `actual_png`，`classification=inconclusive` 由 API 决定，执行器不伪造 diff。
- `baseline.base64` 由 **API** 从内容账本读取后放入请求；执行器不访问文件系统/网络取基线，也不把基线/actual/diff 字节落执行器磁盘。
- `threshold` 0..0.1（NUMERIC(6,5)）；`viewport` 宽高 1..10000；`device_scale_factor` 0.5..4；`selector` ≤500 runes；`region` 归一化 `{x,y,width,height}` 或省略。
- `wait_policy` 可含 network idle/字体/稳定帧覆盖；最终受 `visual.wait_timeout_seconds`（默认 15s，1..120）与 action 超时（`visual.compare_timeout_seconds`，默认 60s，5..300）双重约束。
- **仅该 action** 的请求体上限提高到 `visual.baseline_max_bytes`（DB 设置，默认 5 MiB，64 KiB..20 MiB）；其他 action 与普通路由仍 ≤1MB。超限 → 413 `too_large`。

### 11.2 证据映射与四态

- `actual_png` → `visual_actual`，`diff_png` → `visual_diff`（证据 kind 10 值，`db-schema.md` §13.1）；`screenshot` 仍为通用截图，不复用。
- API 重算 artifact sha256/size（不信任执行器自报），校验 `visual.baseline_max_bytes`，写内容账本（`content_ref`，`owner_kind='evidence'`）后插入 evidence 行。
- 四态：`passed`（diff_ratio ≤ threshold 且环境匹配）、`failed`（超阈值）、`inconclusive`（无基线/环境不一致/尺寸不可比）、`blocked`（通用执行错误导致无法比对，assertion verdict 仍为 `inconclusive` 的契约见 spec §7.3）。
- **环境指纹**：viewport/DPR + selector/region/config_revision + 浏览器名/版本；不一致时 `environment_match=false`，**不返回 diff 图**，判 `inconclusive`（环境不一致不是失败）。
- 幂等重放：同一 `assertion_result_id` 只落一条 `visual_diff`；`execution.visual_assert` 重放返回既有 classification/evidence ids，不重复比对/证据。

### 11.3 等待、失败与安全

- 等待策略：network idle + `document.fonts.ready` + 注入 CSS 冻结动画/过渡/caret + 连续两帧稳定；超时受 `visual.wait_timeout_seconds`/action timeout 限制。
- 失败：页面/导航/context 错误走 §5 通用错误码；环境不一致不按失败处理（`inconclusive`）；执行器重启/context 丢失按 §5 处理。
- 安全：沿用 §2 host allowlist/只读页面/敏感值擦除；不得执行页面脚本外的高危动作；基线字节只在内存中使用，不写执行器磁盘/日志。
- 协议版本：P5 起 action 集合新增 `visual.compare`，协议版本记为 **v1.2**；P3 既有 action 语义/认证/限额不变，P6+ 只允许增量。

## 12. P7 增量：执行器 push 注册/心跳、多端点与容量选择（frozen: P7，2026-11-01）

> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md` §4.5/§6.5/§13。
> P3 **pull healthz**（§7）保留；P7 增加 **push 注册/心跳**、`execution.executor_urls` 多端点 poll 与容量选择。执行器 action 协议仍为 **v1.2**（§11），action/认证/限额/证据映射不变。
> 认证：内部端点使用 DB 设置 `execution.executor_token`（`is_secret=true`）作为 Bearer，不是用户登录 token；常量时间比较，永不写日志/事件/证据。

### 12.1 内部端点（非公开）

| Method/Path | 认证 | Body | 成功响应 |
|---|---|---|---|
| `POST /api/internal/executors/register` | `Authorization: Bearer <execution.executor_token>` | `{endpoint,name,capacity,active_contexts?,version,max_contexts?,status?}` | 200 `{"data": InstanceView}` |
| `POST /api/internal/executors/heartbeat` | 同上 | `{endpoint,active_contexts,capacity?,version?,status?}` | 200 `{"data": InstanceView}` |

- `register`：执行器启动注册。`endpoint` 必填、绝对 http(s)、≤500（唯一键；尾部 `/` 归一化后去重）；`name` 必填 1–200（`host:port`）；`capacity`（=执行器 `maxContexts`）必填 ≥1，`max_contexts` 为可选同义别名（同时给出时必须相等）；`active_contexts` 可选 ≥0（默认 0）；`version` 必填 ≤64；`status` 可选 `active|degraded`（仅记录健康信号，不改变持久化状态）。服务端 upsert `executor_instance`（`token_hash=sha256(token)`、`last_heartbeat=now`、`last_error=''`）；状态：已有 `draining` 保持，其余置 `active`。重复 register 幂等。
- `heartbeat`：执行器周期心跳。`endpoint` 必填且必须是已注册端点；更新 `active_contexts`、`capacity`（可选）、`version`（可选）、`last_heartbeat=now`；`status` 仅允许 `active|degraded`，`degraded` 只记录告警/`last_error`，**不**因一次 degraded 置 offline；DB 状态 `draining` 保持、其余恢复 `active`（含 offline 恢复）。未知 endpoint → **404 `executor_unknown`**，不自动创建行。
- `InstanceView`：`{instance_id,endpoint,name,status,capacity,active_contexts,version,last_heartbeat}`；ID 为十进制字符串，时间 RFC3339 UTC。
- 错误：400 `invalid_argument`（字段/长度/URL/capacity/status）、401 `unauthorized`（缺失/错误 token）、404 `executor_unknown`（heartbeat 未知端点）；统一错误信封。
- 不提供 unregister；离线由 `execution.executor_offline_seconds` 判定；`draining` 只能由 admin API（`/api/admin/executors/{id}/drain|activate`）改变，push 注册/心跳不得清除 draining。

### 12.2 多端点 poll（`execution.executor_urls`）

- 端点集合 = `execution.executor_url` ∪ `execution.executor_urls`（逗号分隔）。逐项 trim、去尾部 `/`、按绝对 http(s) 校验、去重；空项忽略；无效项记日志并跳过，不阻塞启动。
- 每个端点每 `execution.executor_heartbeat_seconds`（默认 10s，1–60）调用 `GET {endpoint}/healthz`，成功按 P3 §7 upsert 同一 `executor_instance`（`endpoint` 唯一；`capacity=healthz.maxContexts`；`active_contexts=healthz.contexts`；`version=healthz.version`；`last_heartbeat=now`）；失败超过 `execution.executor_offline_seconds`（默认 30s，5–300）置 `offline`。
- poll 与 push 写同一行：push 注册的端点即使不在 poll 集合中，也通过 heartbeat 保活；poll 成功同样恢复 `active`（`draining` 保持）。
- `execution.executor_urls` 为空时等价 P3 单端点；`execution.executor_token` 用于内部 push 路由（poll 的 `GET /healthz` 按 P3 §3 无 auth）。

### 12.3 容量选择与排空

- **P8 已实现（P8-D-11，per-executor 路由）**：`execution.executor_urls` 非空时，全部端点参与 poll/注册/心跳/管理端列出，并从所有可派发行中按容量选择；派发不再限主端点。组装根使用完整 `Registry`（含 push 注册端点）替换 `primaryExecutorRegistry`，`Registry.Select` 的 `capacity DESC, last_heartbeat DESC, id ASC` 排序生效；`owner_executor_id` 记录真实执行器，`case_run_sut_context.executor_id` 落库。
- **线协议不变**：仍为 **v1.2**，不新增/修改 action；`BrowserExecutor` 端口按 `executorID` 路由 `CreateContext`/`Action`/`Cancel`/`Close`，HTTP 请求/响应/认证/限额/证据映射与 §11 不变。
- 单端点兼容：`execution.executor_urls` 为空时保持 P3 语义，只从 `execution.executor_url` 对应端点行派发；push 注册的其它端点只保活、不参与派发（P8 不改变空列表语义）。
- 调度只从 `status='active' AND active_contexts < capacity` 的行中选择；per-executor 派发按 `capacity DESC, last_heartbeat DESC, id ASC` 排序（容量大者优先、最近心跳优先、稳定 id）。
- `capacity=0` 永不选中；`draining` 停止新派发、在飞 case_run 继续；`offline` 不派发，pending 等待恢复，running 由 reconciler 按 `interrupted(executor_lost)` 且 `dispatch_epoch++` 重派（最多 3 次，沿用 P3）。
- 无可用执行器 → 派发返回 503 `executor_unavailable`（沿用 P3 错误语义）；degraded 只影响告警/派发前复检，不直接排除。
- 不实现跨执行器上下文迁移；一个 case_run 的同一 `(version_sut_id, account_role)` context 仍在同一执行器内复用（§6）。

### 12.4 执行器 push 客户端

- 执行器进程新增可选 env `EZTEST_API_URL`（EzTest API 基址；空/未设置则禁用 push）；使用既有 `EZTEST_EXECUTOR_TOKEN` 作为 Bearer。`EZTEST_EXECUTOR_HEARTBEAT_SECONDS`（默认 10，1–60）控制 push 心跳间隔；广告 endpoint 由 `EZTEST_EXECUTOR_ADDR` 归一化为绝对 http(s) URL。**EzTest API 启动 env 仍严格 4 项**，这些均属于执行器进程配置。
- 启动时 `register`（`endpoint/name/capacity/active_contexts/version`；`name` 缺省用 endpoint）；之后每 `EZTEST_EXECUTOR_HEARTBEAT_SECONDS` 发 `heartbeat`（携带当前 `active_contexts/capacity/version`，内部 degraded 时 `status="degraded"`，否则 `status="active"`）。注意 push 心跳节奏由执行器本地 env 决定，不由 API 的 `execution.executor_heartbeat_seconds`（那是 API 侧 pull poll 间隔）决定。
- push 失败（网络/401/404/5xx）按 1s 起、上限 30s 的指数退避重试，只记日志/`last_error`，**不阻断**用例执行；不因 push 失败删除 context 或中断在飞 action。
- 无 unregister/优雅摘流 push；滚动升级仍由 admin drain + `GET /api/health/ready` 执行。

### 12.5 约束

1. P3 pull、P5 `visual.compare`、action/证据/限额语义不变；P7 不新增 action。
2. 内部端点只接受 `execution.executor_token` Bearer；不得暴露给前端/普通用户；token 不落日志/事件/证据。
3. `draining` 保持、未知 heartbeat endpoint 404、`executor_urls` 去重/无效项跳过是冻结语义。
4. 多端点/多副本不改变事件频道与 SSE 语义；`executor_instance` 表结构不变（P3 已含 capacity/active_contexts/last_heartbeat/version/last_error）。
