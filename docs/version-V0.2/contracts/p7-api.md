# EzTest 0.2 P7 API 契约（draft: P7）

> 状态：`draft: P7`（2026-11-01；Task 20 翻为 `frozen: P7`）。
> 来源：`docs/superpowers/specs/2026-11-01-eztest-v02-p7-p9-design.md` §6/§13。
> 说明：本文件冻结 P7 增量 API 的 method/path/operationId/权限/错误码与视图字段；`openapi.yaml` 的 P7 paths/schemas 由 Lead 在实现与冻结任务写入，须与本文件逐项一致。
> 通用约定沿用 P0：成功 `{"data": ...}` / 列表 `{"data": [...], "next_cursor", "has_more"}`；错误信封 `{"error":{"code","message","request_id","retryable","details"}}`；ID 为十进制字符串；时间 RFC3339 UTC；除标注公开/内部外均要求登录 Bearer。

## 1. 公开 API（16 个，draft: P7）

| operationId | method/path | 权限 | 语义/成功码 | 错误码（除 401 外） |
|---|---|---|---|---|
| `listKnowledgeItems` | `GET /api/knowledge/items?scope=&status=&system_key=&tag=&q=&cursor=&limit=` | auth | 列表/全文检索（`ts_rank` + id 游标；limit 默认 20、上限 100）；200 | 400 `invalid_argument`（scope/status 组合、cursor/limit） |
| `createKnowledgeItem` | `POST /api/knowledge/items` | auth | 创建 `draft`（body `title/content/scope?/system_key?/tags?/source_version_id?/promoted_from_version_id?`）；201 | 400 `invalid_argument`（长度/scope/system_key/tags） |
| `getKnowledgeItem` | `GET /api/knowledge/items/{knowledgeId}` | auth | 详情 + 最近 revisions + 读取期 `source_stale`；200 | 404 `not_found` |
| `updateKnowledgeItem` | `PATCH /api/knowledge/items/{knowledgeId}` | 作者或 admin，且 status∈draft/rejected | 编辑草稿：写 `knowledge_revision`、`revision+1`、`rejected→draft`；200 | 400 `invalid_argument`；403 `forbidden`；404 `not_found`；409 `state_conflict` |
| `approveKnowledgeItem` | `POST /api/knowledge/items/{knowledgeId}/approve` | admin | `draft/revoked → approved`；`review_required=false` 时允许 admin 一步；200 | 404 `not_found`；409 `state_conflict` |
| `rejectKnowledgeItem` | `POST /api/knowledge/items/{knowledgeId}/reject` | admin | `draft → rejected`；body `reason` 必填；200 | 400 `invalid_argument`（reason 缺失/超长）；404 `not_found`；409 `state_conflict` |
| `revokeKnowledgeItem` | `POST /api/knowledge/items/{knowledgeId}/revoke` | admin | `approved → revoked`；body `reason` 必填；200 | 400 `invalid_argument`；404 `not_found`；409 `state_conflict` |
| `listKnowledgeRevisions` | `GET /api/knowledge/items/{knowledgeId}/revisions` | auth | 追加式内容历史（`revision DESC`）；200 | 404 `not_found` |
| `createGCRun` | `POST /api/admin/gc/runs` | admin | body `{mode:"dry_run"/"apply", confirm?:bool}`；apply 需 `confirm=true`；返回 run（**202** 语义） | 400 `confirmation_required`/`invalid_argument`；409 `gc_running`；403 `forbidden` |
| `listGCRuns` | `GET /api/admin/gc/runs?limit=` | admin | 运行历史（`started_at DESC, id DESC`）；200 | 400 `invalid_argument` |
| `getGCRun` | `GET /api/admin/gc/runs/{gcRunId}` | admin | 详情 + `report` JSONB 候选/删除明细 + tombstones；200 | 404 `not_found` |
| `listInstances` | `GET /api/admin/instances` | admin | API 实例列表（node_id/status/heartbeat/pid/port/version）；200 | 403 `forbidden` |
| `drainInstance` | `POST /api/admin/instances/{instanceId}/drain` | admin | 排空：readiness 503、停止认领新任务；in-flight 完成；200 | 404 `not_found`；409 `state_conflict` |
| `activateInstance` | `POST /api/admin/instances/{instanceId}/activate` | admin | 取消排空回到 active；200 | 404 `not_found`；409 `state_conflict`（stopped） |
| `getPlatformReadiness` | `GET /api/health/ready` | 公开 | DB ping + 存储写探针 + 非 draining；200/503；成功/失败均为 `{"data":{...}}`（沿用 `/api/health` 信封） | 无（失败用 503 + `data.status=degraded`） |
| `downloadReportPDF` | `GET /api/versions/{versionId}/reports/{reportId}/revisions/{revision}/pdf` | auth（版本可读同 HTML 下载） | 200 `application/pdf` + `ETag`（内容 sha256）+ `Content-Disposition` + `Cache-Control: private, no-store` + `X-Content-Type-Options: nosniff` | 404 `not_found`（不存在/未生成/disabled）；409 `pdf_not_ready`（pending/running）、`pdf_failed`（failed） |

- operationId 说明：既有 `GET /api/health` 已占用 `getReadiness`，P7 的 `/api/health/ready` 使用 `getPlatformReadiness`；其余命名沿用既有动词前缀（`list/create/get/update/approve/reject/revoke/drain/activate/download`）。
- OpenAPI tag 归属：知识 8 个 → `Knowledge`；GC 3 + 实例 3 → `Admin`；PDF 1 → `Delivery`；readiness 1 → `Health`；内部 2 → `Internal`（非公开，不加入用户客户端）。
- 知识列表/详情/历史均登录可读；审核写 admin-only；`source_stale` 是读取期布尔，不落库、不新增状态。
- GC 的 `apply` 单活跃运行由 `uk_gc_run_running` 保证；重复 apply 冲突 409 `gc_running`；`dry_run` 任意时刻可跑。
- 实例 drain/activate 是幂等目标态操作（已 draining 再 drain 返回 200 且不变；已 active 再 activate 同理）；`stopped` 不可 activate。

## 2. 内部 API（2 个，draft: P7）

| operationId | method/path | 认证 | 语义/成功码 | 错误码 |
|---|---|---|---|---|
| `registerExecutor` | `POST /api/internal/executors/register` | Bearer `execution.executor_token`（非用户 token） | 执行器启动注册：upsert `executor_instance`；draining 保留、其余 active；200 `{"data":InstanceView}` | 400 `invalid_argument`；401 `unauthorized` |
| `heartbeatExecutor` | `POST /api/internal/executors/heartbeat` | 同上 | 心跳：更新 `active_contexts/capacity/version/last_heartbeat`；draining 保留、其余 active（含 offline 恢复）；200 `{"data":InstanceView}` | 400 `invalid_argument`；401 `unauthorized`；404 `executor_unknown` |

- 请求字段与完整语义见 `executor-protocol.md` §12；内部端点不得暴露给前端/普通用户，token 不写日志/事件/证据。
- `InstanceView`：`{instance_id,endpoint,name,status,capacity,active_contexts,version,last_heartbeat}`（ID 字符串、时间 RFC3339 UTC）。

## 3. 视图字段变更（draft: P7）

| 视图 | 变更 | 说明 |
|---|---|---|
| `ReportRevisionView` | `pdf_available` **扩宽为 boolean**（原恒 `false`）；新增 `pdf_status`、`pdf_error`、`pdf_rendered_at` | `pdf_available=true` ⇔ `pdf_status='done'` 且 `pdf_ref` 非空；`pdf_status ∈ pending/running/done/failed/disabled`；`pdf_error` 可空（≤2000）；`pdf_rendered_at` 可空 RFC3339 UTC |
| `IssueView`（含详情/全局复用） | 新增 `recurrence_count`、`last_recurred_at`、`merged_chain` | `recurrence_count` 整数 ≥0；`last_recurred_at` 可空；`merged_chain` 可选十进制字符串数组（源→目标，≤16，读取期计算） |
| `SUT` / SUT 写模型 | 新增可选 `family` | 可空、小写 slug 1–64；旧数据 NULL；读/写均返回；用于 `knowledge.scope='system'` 匹配 |
| `KnowledgeItemView` | 新增 | `id/scope/system_key?/title/content/tags/status/confidence?/source_version_id?/promoted_from_version_id?/revision/review_reason?/reviewed_by?/reviewed_at?/revoked_at?/source_stale/created_by?/created_at/updated_at`；`source_stale` 只读 |
| `KnowledgeRevisionView` | 新增 | `id/knowledge_id/revision/title/content/tags/changed_by?/created_at`；append-only |
| `GCRunView` | 新增 | `id/mode/status/actor_id?/actor_kind/started_at/finished_at?/scanned_objects/scanned_refs/deleted_objects/deleted_refs/deleted_bytes/report/error?/created_at/updated_at` |
| `InstanceView` | 新增 | 见 §2；`status ∈ active/draining/offline/stopped` |
| `GET /api/health/ready` body | 新增 | `{"data":{status:"ok"/"degraded",database:"ok"/"error",storage:"ok"/"error",draining:bool,instance_id:"..."}}`；任一失败或 draining → 503 |

- 字段均为**可加扩宽**：P0–P6 已有字段/语义/枚举不删除、不重命名；只扩展 PDF、复发、SUT family 与新增资源视图。
- PDF 下载不复用 HTML 的 `as_of`/`html_ref`：PDF 字节以内容账本为权威，`ETag` 为 PDF sha256；不返回 `content_ref`/内部路径。
- 错误信封/`X-Request-ID`/分页信封与 P0 一致；业务冲突用 `state_conflict`，稳定子码放在 `details.reason`（如 `invalid_transition`、`pdf_not_ready`、`pdf_failed`、`gc_running`）。

## 4. 稳定错误码补充（draft: P7）

| code | HTTP | 触发 |
|---|---|---|
| `confirmation_required` | 400 | GC `mode=apply` 未带 `confirm=true` |
| `gc_running` | 409 | 已有 running 的 GC run |
| `pdf_not_ready` | 409 | PDF 下载时 `pdf_status IN (pending,running)` |
| `pdf_failed` | 409 | PDF 下载时 `pdf_status='failed'` |
| `executor_unknown` | 404 | 内部 heartbeat 的 endpoint 未注册 |
| `state_conflict` | 409 | 知识状态迁移/实例状态迁移非法（`details.reason` 给稳定子码） |

其余沿用既有 `invalid_argument`、`unauthorized`、`forbidden`、`not_found`、`executor_unavailable`、`provider_unavailable` 等；P7 不新增通用 `Idempotency-Key`。
