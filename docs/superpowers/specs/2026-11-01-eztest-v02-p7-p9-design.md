# EzTest 0.2 P7（P9 五项）阶段设计规格

> 状态：**draft（待用户复核）**
> 日期：2026-11-01
> 阶段：P7 / `docs/version-V0.2/phases/phase-07-p9.md`
> 实施分支：`feat/v0.2-p7`（从 `feat/v0.2` @ `a1bd99b` 拉出；验收后合回 `feat/v0.2`）
> 权威来源（只读）：`docs/version-V0.2/architecture-design.md` §12/§13、`high-level-design.md` §9/§10/§11、`research/decision-log.md`（D32/D33/D34/D40/D49–D53）、`research/open-decisions.md`（OD-2/OD-3/OD-4/OD-11）、`contracts/*`（P6 frozen）、`ui/mock.html`。

---

## 1. 目标、范围与成功标准

### 1.1 目标

补齐 0.2 的 P9 五项（`phase-07-p9.md:7`）：

1. **D59** 报告 HTML/PDF：`report_revision` HTML 权威 + 独立 headless 渲染 worker 出 PDF + 内置模板/章节开关 + 渲染状态/重试。
2. **D63** 跨版本知识：`global`/`system` scope、显式晋升、管理员审核、澄清/设计注入（带引用与预算）、撤回/复审。
3. **D65** 统一内容账本 GC：附件/导出/报告/孤儿、dry-run、引用保护、24h 宽限、保留期、tombstone/审计。
4. **D67** 多副本安全 + 基础编排：lease/fencing（复用）、PG LISTEN/NOTIFY 跨实例 SSE、共享存储、readiness/排空/滚动升级、执行器注册/容量/离线重派。
5. **问题**：重开/合并（P4 已有）+ **合并后复发的重定向**、跨波次去重稳定性、旧报告 as-of 一致性、UI 补齐。

### 1.2 In（本阶段必须交付）

- 迁移 #34–#39（见 §5）与 `order.txt`。
- 新限界上下文 `backend/internal/knowledge/`（四层齐全）。
- 新限界上下文 `backend/internal/orchestration/`（实例注册/leader/readiness/排空；四层齐全）。
- `delivery`：PDF 渲染任务/状态/下载；问题复发重定向；报告附录章节与模板版本 2。
- `storage`：GC 扫描/引用保护/dry-run/tombstone/审计 + admin API + 周期 worker。
- `eventing`：跨实例 fan-out（LISTEN/NOTIFY）；`health`：readiness/drain。
- `executor/`：多端点拉取 + 最小 push 注册/心跳；`renderer/` 新独立 Node 服务。
- 前端：`/knowledge` 实页（列表/详情/审核/撤回/复审）；报告 PDF 状态与下载、附录；设置页 GC 面板与实例面板；问题复发/合并链展示；SUT family 字段。
- 契约冻结 `frozen: P7`：openapi/events/db-schema/migrations-order/settings/task-types/agent-profiles-tools/executor-protocol/README。
- 验收脚本与证据：`scripts/p7-acceptance.ps1`、`scripts/p7-e2e.ps1`、`scripts/p7-contract-check.cjs`、`scripts/p7-multi.ps1`、`docs/version-V0.2/acceptance/p7/`（含截图）。
- `docs/version-V0.2/phases/phase-07-handoff.md`。

### 1.3 Out（本阶段不做）

- 0.3 新能力：多用户协作/站内通知、非 PG、外部工单/CI/IM、自动扩缩容、多区域 HA、对象存储、向量 RAG、自定义报告模板编辑器（`architecture-design.md:529`、`decision-log.md:137-142`）。
- 问题：跨版本合并、撤销合并、merged 状态重开（`high-level-design.md:638-640`）。
- 事件日志 GC / 审计日志清理 / 限流（`events.md:38`、`retention.*` 未定义事件保留；记录为 backlog）。
- 0.1 数据只读归档/导出脚本（phase 文件标注“可选交付”，记录为 backlog）。
- 报告 diff（HLD §7.1 提及但 §9.1 未定义，记录为 backlog）。
- 执行器 push 之外的 VNC/storage_state/wait_until（`executor-protocol.md:132-136` 后续项）。

### 1.4 成功标准（对应 `phase-07-p9.md:42-48`）

1. 报告 HTML/PDF、模板/章节、revision/as-of/stale 可演示；PDF 由独立进程生成，失败可重试且不阻塞 HTML。
2. 知识晋升/审核/注入/撤回可演示；注入带引用与预算；`scope=global|system` 生效。
3. 多副本：加副本/滚动升级/故障恢复不重复执行、不批量中断；SSE 跨实例；执行器离线重派。
4. 问题重开/合并/复发重定向/跨波次去重/旧报告 as-of 可演示。
5. `scripts/test.ps1` 全绿、P2–P7 契约门禁全绿、验收脚本全 PASS；contracts `frozen: P7`；handoff 完成。

---

## 2. 现状核对（HEAD `a1bd99b`，已逐项核验）

| 能力 | 现状 | 证据 |
|---|---|---|
| report/report_revision + `pdf_ref`/`render_status`/`template_version`/`as_of` | **存在** | `backend/internal/db/migrations/delivery_0002_report_revision.sql:38-60` |
| HTML 权威渲染（内置模板 v1，5 章节） | **存在** | `backend/internal/delivery/infrastructure/render/render.go:22-31,:46-59`、`template.go:8-168` |
| 章节开关 `report.section_*` | **存在（启动读一次）** | `settings/application/catalog.go:132-136`、`cmd/eztest/p4_wiring.go:122-144` |
| PDF 生成/渲染 worker/PDF 下载 | **缺失** | `pdf_available` 恒 false：`contracts/openapi.yaml:9957-9960`；DTO `delivery/interfaces/httpapi/dto.go:363` |
| 模板版本化/附录 | **部分**（渲染器只接受 `"1"`，启动 `log.Fatalf`） | `render.go:46-49`、`cmd/eztest/main.go:737-744` |
| 知识（表/上下文/API/注入/UI） | **缺失（0.2 全新）** | 无 `knowledge` 迁移/包/路径；`frontend/src/app/(app)/knowledge/page.tsx:7-14` 占位 |
| content_object/content_ref 账本 | **存在** | `db/migrations/storage_0001_content_ledger.sql:1-20` |
| GC（扫描/引用保护/dry-run/tombstone/审计/API） | **缺失**；`retention.*` 键未被消费 | `settings/application/catalog.go:124-128`；无 `gc_*` 表/包 |
| work_item lease/fencing、agent_run/case_run epoch | **存在** | `agent_0001_work_item.sql:4-41`、`execution_0001_intent_wave.sql:70-103` |
| 执行器注册/心跳/容量/排空/离线重派 | **存在（单端点 pull）** | `execution/application/registry.go:94-233`、`execution/application/recovery.go:109-207` |
| 跨实例 SSE | **缺失（进程内 Hub）** | `eventing/application/hub.go:13-15` |
| API 实例注册/leader/readiness/排空 | **缺失**；所有 tick 每进程无条件启动 | `cmd/eztest/main.go:895-936`、`server/health.go:44-70` |
| 雪花节点每副本唯一 | **缺失**（单一设置默认 1） | `settings/application/catalog.go:70`、`config/config.go:21-31` |
| 问题重开/合并/合并环检测/跨波次 dedupe/as-of | **存在** | `delivery/application/issue_service.go:263-354`、`delivery/domain/issue.go:20-122`、`delivery_0001_issue_lifecycle.sql:8-38` |
| 合并后复发重定向/别名/复发计数 | **缺失** | `issue_service.go:444-473`（dedupe 命中直接返回源 issue） |
| 共享存储配置/校验、多副本部署 | **缺失**（单副本 compose） | `deploy/docker-compose.yml:33-139` |

**已接受的基线偏差**（不属 P7 修复范围，记录于 handoff/风险）：
- CI `static-acceptance` 运行 0.1 `scripts/acceptance.ps1 -StaticOnly`（`.github/workflows/ci.yml:134-135`），与 0.2 树不一致；P7 不修 0.1 harness，但必须保证 `scripts/test.ps1` 与 P7 门禁绿。
- `deploy/` 中还有 0.1 env/卷声明（`deploy/docker-compose.yml:67-71`、`Dockerfile.api:50-56`）；P7 更新 compose 到 0.2 形态（见 §11）。
- `D59/D63/D65/D67` 在 0.2 `decision-log.md` **没有独立决策行**，规范内容只存在于 `architecture-design.md:525-562`、`high-level-design.md:609-640` 与 OD-2/3/4/11；本 spec 引用这些章节而非虚构决策号。

---

## 3. 设计缺口裁定（用户已批准 2026-11-01）

| ID | 缺口 | 裁定 | 备选与否决理由 | 契约影响 |
|---|---|---|---|---|
| P7-D-01 | `scope=system` 的“SUT 族”无实体（`architecture-design.md:535`、`high-level-design.md:618`） | `sut` 增加可选 `family TEXT`（小写、≤64）；`knowledge_item.scope=system` 用 `system_key = family`；注入匹配版本绑定的 SUT family | 否决“system 仅按 sut_id”（语义偏离“族”）；否决“不做 system”（违背 OD-3） | 新增列 + SUT API 可选字段（可加） |
| P7-D-02 | PDF 渲染拓扑未定（`architecture-design.md:528`、OD-2） | 新增独立 Node 服务 `renderer/`（Playwright + 已安装 Chrome channel，`POST /v1/pdf`，Bearer token）；API 侧以 **`report.render_pdf` work_item** 驱动（版本域，复用 lease/fencing）；PDF 字节入 content ledger（`owner_kind=report_revision`、`role=report_pdf`） | 否决“API 进程内渲染”（违背独立 worker、API 不装 Chromium）；否决“executor 加 PDF action”（执行器职责漂移） | 新链路 + 事件 + 设置；`pdf_ref` 已预留（`db-schema.md:909`） |
| P7-D-03 | 模板版本化与附录（OD-2 六章节，P4 只有五章节） | 仍为**单套内置模板**；内置模板支持 `template_version` `"1"`（既有五章节）与 `"2"`（+附录）；新设置 `report.section_appendix`；新库默认 `"2"`，既有库可保持 `"1"` 或由管理员切换；旧 revision 永不重渲染 | 否决“模板表/CRUD”（0.2 明确不做自定义模板编辑器）；否决“直接改 v1”（版本化失真） | `report.template_version` 默认值随新库变更；新增设置键 |
| P7-D-04 | GC 任务与墓碑模型未定（`architecture-design.md:542-546`） | 专用 `gc_run` + `gc_tombstone` 表 + 周期 worker（leader 调度）；`retention.gc_dry_run` 默认 true；apply 必须 `confirm=true`；报告 revision 的 HTML/PDF 永不自删；“旧 PDF”= 被替代导出 PDF（`export_artifact` 旧 revision），非报告 revision PDF | 否决“塞进 work_item”（`work_item.version_id NOT NULL`，`db-schema.md:476,:1349`）；否决“物理立即删除”（设计要求 tombstone/审计） | 新表/新 API/新设置；`content_ref.deleted_at` 复用 |
| P7-D-05 | 多副本编排未定（`architecture-design.md:548-555`） | 新上下文 `orchestration`：`api_instance` 注册/心跳/排空 + **PG advisory-lock leader**（reconciler/scheduler/executor-poller/GC 调度仅 leader 执行）；雪花 `node_id` 从注册表分配（优先 `runtime.snowflake_node_id`，冲突取最小空闲 0–1023）；readiness 校验 DB + 存储可写 + 非排空；共享存储按 NFS/等价物契约（单机双进程演练用同目录等价） | 否决“每个 tick 都做 CAS”（工作量大且非本阶段目标）；否决“靠唯一约束兜底”（scheduler 非 CAS，重复派发风险已核验） | 新表/新 API/新事件/新设置；compose 多副本 |
| P7-D-06 | 权限（0.2 无 RBAC） | 复用“管理员 + 普通用户”：知识审核/撤回/复审、GC apply、实例 drain/activate 仅管理员；普通用户可建知识草案；全局 `/issues`、`/reports` 保持只读；问题确认/驳回/重开/合并仍在版本 Tab | 否决“普通用户直接发布 global 知识”（违背审核）；否决“全局中心可写”（违背 HLD `:524`） | 权限说明入契约 |
| P7-D-07 | `knowledge.review`/`gc.run` 无法作 work_item | 知识审核是管理员同步动作（不建任务）；GC 用 `gc_run` 表；（`report.render_pdf` 因版本域可作 work_item，沿用预留名） | `work_item.version_id NOT NULL`（`db-schema.md:476`） | task-types 只注册 `report.render_pdf`；偏差记录 handoff |
| P7-D-08 | 合并后复发重定向语义（`high-level-design.md:639`） | 读时沿 `merged_into_id` 链（≤16 跳）重定向到目标；向目标追加 `issue_ref(role='recurrence')`、`recurrence_count+1`、`last_recurred_at`、事件/审计；**不**迁移历史 refs、**不**建别名表、**不**重开源 issue | 否决“迁移 refs”（破坏 append-only 语义与旧报告 as-of）；否决“别名表”（多一张表无收益） | `delivery_issue` 新列；`issue_ref.role` 自由文本无需迁移 |
| P7-D-09 | 执行器 push（`executor-protocol.md:136` 预留） | 保留 pull 为主（P3-D-01）；新增多端点设置 `execution.executor_urls`；同时实现最小 push `POST /api/internal/executors/register` 与 `/heartbeat`（同一 token，executor 启动/周期上报） | 否决“只做 push”（破坏 P3 拉取心跳语义） | 新内部路径 + 协议小节；executor 客户端 |
| P7-D-10 | 0.1 数据导出脚本（phase 文件“可选”） | 不做，记录 backlog（P8 若需要另行设计） | 与 0.2 新库决策无冲突，但无验收入口 | 无 |

---

## 4. 总体方案

### 4.1 进程/上下文拓扑

```
web (Next.js) ── HTTP/SSE ──> api (Go, 可多副本, 共享 PG + 共享 .data)
                                 │  ├─ leader: reconciler / execution scheduler / executor poller / GC scheduler
                                 │  ├─ all: agent worker / codeintel worker / report.render_pdf worker / SSE(LISTEN/NOTIFY)
                                 │  └─ orchestration: api_instance 注册/心跳/readiness/drain
                                 ├─ HTTP ──> executor (Node/Playwright, pull+push)
                                 └─ HTTP ──> renderer (Node/Playwright, 只做 HTML→PDF)
PostgreSQL（唯一协调面：lease/fencing、advisory lock、LISTEN/NOTIFY、事件/审计）
共享文件存储（paths.data_root；单机=同一目录，生产=NFS/等价物）
```

### 4.2 D59 报告 PDF（链路）

1. 报告发布（既有）→ revision 的 `pdf_status='pending'`（新列，默认 pending）。
2. reconciler 新规则（P7 ensurer）：revision `status=published` 且 `render_status='done'`、`report.render_pdf=true`、`pdf_status in (pending,failed)` 且尝试未超限 → ensure work_item `report.render_pdf`（`team=delivery`、`dedupe_key=report:{reportID}:rev:{revision}`、payload `{report_id, report_revision_id}`）。
   - 迁移 #36 的列默认值 `pending` 会把**既有已发布 revision** 纳入补齐队列（P9 要求旧报告也能出 PDF）；补齐受 `retention.gc_batch_size` 无关的每轮扫描上限（reconciler 现有 `reconcilerLimit=100`）约束，可在 `report.render_pdf=false` 时整体关闭。设计记录为“历史 revision PDF 补齐”，不是删除/重写内容。
3. `delivery` 新增 `RenderWorker`：按 type 认领（新 work_item 认领方法 `ClaimForTypes`），带心跳续租；读已冻结 HTML（content ledger，owner=`report_revision`/role=`report_html`）→ POST renderer `/v1/pdf` → 校验 `%PDF-` 魔数与大小上限 → 写入 ledger（owner=`report_revision`/role=`report_pdf`）→ 受 fence 的元数据更新（`pdf_status='done'`、`pdf_ref`、`pdf_rendered_at`、`pdf_attempts++`）。
4. 失败：可重试错误 → work_item retry（指数退避由 work_item 提供）；超过 `report.render_max_attempts` → `pdf_status='failed'`、`pdf_error`、事件 `report.pdf_failed`；reconciler 不再自动补（管理员可通过 regenerate/re-enable 触发新 revision）。
5. 关掉 `report.render_pdf` → 新 revision `pdf_status='disabled'`，不建任务；已生成 PDF 不删。
6. HTML 发布**不等待** PDF；PDF 生成失败不影响 `report published` 与版本收尾谓词（D07 只要求 report published，`decision-log.md:28`）。
7. 下载：`GET .../revisions/{revision}/pdf`，鉴权与 HTML 下载一致，校验 ledger owner/role/sha/size，返回 `application/pdf` + ETag + Content-Disposition；PDF 缺失 404/409 语义见 §6。

### 4.3 D63 知识（模型与流程）

- **状态机**：`draft → approved | rejected`；`approved → revoked`；`revoked → approved`（复审）；`rejected → draft`（编辑后重新提交）。`stale` 不单独建状态（设计冲突 A1/A6），来源版本不存在时由**读取期**标注 `source_stale=true` 并从注入排除（`architecture-design.md:538`）。
- **scope**：`global`（`system_key NULL`）或 `system`（`system_key = sut.family`）。
- **晋升**：`POST /api/knowledge/items` 创建 draft（可带 `source_version_id` 与 `promoted_from_version_id`，后者默认等于前者）表示“从版本来源晋升”；AI 通过 `knowledge.propose` 工具创建 draft（仅候选，不能审核）。
- **审核**：`review_required=true`（默认）→ draft 只有管理员 approve 才注入；`review_required=false` → 管理员创建/晋升时可一步 approve（AI 提议仍 keep draft）。
- **注入**：`version_pack.knowledge_refs`（`high-level-design.md:386`）由 `versionadapter` 从 knowledge 应用服务取得：`status=approved` + (`scope=global` 或 `system_key ∈ 版本 SUT families`)，按版本标题/需求标题做 PG 全文 + `ts_rank`，受 `knowledge.injection_budget_tokens` 与 `knowledge.injection_max_items` 约束；渲染为 context `knowledge` 章节，带 `[K-<id>]` 引用；`knowledge_usage` 记录 run/条数/token 估算。
- **撤回/复审**：revoke 立即从后续注入排除；复审 approve 恢复；来源版本删除不存在（0.2 无删除版本），来源缺失时 `source_stale` 过滤。
- **检索**：PostgreSQL `tsvector` 生成列 + GIN，不做向量 RAG（`architecture-design.md:537`）。

### 4.4 D65 GC（账本扫描与保护）

**候选集（四类）**
1. `unreferenced`：`content_object` 无任何 `content_ref.deleted_at IS NULL`，且 `content_object.created_at < now - max(unreferenced_content_days, gc_grace_hours)`。
2. `temp`：`.data/objects` 下存在但 `content_object` 无对应行的孤儿文件，且 mtime 早于 `retention.temp_hours`。
3. `superseded_export`：`export_artifact` 同一 `(version_id, owner_kind, owner_id, file_format)` 下 `revision < max(revision)`，且 `created_at` 早于 `replaced_export_days`；删除其 `content_ref(owner_kind='export', owner_id=artifact.id)`，对象若无其他活引用再按 1 处理。
4. `orphan_ref`：`content_ref` 的 owner 实体已不存在（按 owner_kind 枚举存在性检查），soft-delete 该 ref（对象随后落入 1）。

**保护（永不自动删）**
- `report_revision` 的 `report_html` 与 `report_pdf`（旧 revision 不删，OD-4）。
- `requirement_attachment` 且 attachment 行与 version 行均存在。
- `evidence` 且 evidence 行存在。
- 当前（非被替代）`export_artifact`。
- 有活 ref 的任何对象（按对象粒度的引用计数）。
- 任何落在宽限期（`retention.gc_grace_hours`，默认 24）内的内容。

**执行**
- `gc_run(mode=dry_run|apply)`；dry-run 只写报告；apply 需 `confirm=true`，逐对象先写 tombstone 再删 `content_object` 行与磁盘文件（文件缺失视为已删，记录 `missing`）；删除顺序：ref → object 行 → 文件。
- 周期 worker 由 leader 按 `retention.gc_interval_minutes` 触发，mode 取 `retention.gc_dry_run`；manual API 任意时刻可跑 dry-run。同一时间只允许一个 running `gc_run`（部分唯一索引/leader 保证）。
- 审计 `storage.gc.completed`、事件 `gc.completed`（system 频道）带 counts。

### 4.5 D67 多副本（原语复用 + 新编排）

- **实例注册**：启动即插入 `api_instance`（`instance_key = hostname:pid`），分配 `node_id`（优先 `runtime.snowflake_node_id`，被占用取最小空闲 0–1023）；每 `instance.heartbeat_seconds` 心跳；优雅退出置 `stopped` 并释放 node_id；超过 `instance.offline_seconds` 无心跳 → `offline`（node_id 可被回收，风险见 §14）。
- **leader**：`pg_try_advisory_lock(0x455A5431)` 独占连接，持锁期间 `IsLeader()=true`，连接断开/退出自动释放；`RunIfLeader` 控制 reconciler / execution scheduler+recovery / executor poller / GC scheduler。leader 切换不中断 in-flight work_item（lease 继续有效，其它实例可认领）。
- **SSE 跨实例**：事件提交后 `pg_notify('eztest_events', <event_id>)`；每实例一个专用 `LISTEN` 连接，收到 id 后从 `event` 表读取并投递本地 Hub；本地 Hub 仍负责订阅者；`Last-Event-ID` 回放不变；NOTIFY 丢失由 SSE 客户端重连回放兜底。事件写入与 NOTIFY 同事务（`pg_notify` 在事务提交时投递）。
- **readiness/排空/滚动升级**：`GET /api/health/ready`（DB ping + 存储可写探针 + 实例非 draining）；`POST /api/admin/instances/{id}/drain|activate`；drain 后 readiness 503、worker 不再认领新任务（agent/render/codeintel worker 每轮检查 drain）、in-flight 完成；滚动升级：起新副本 → 等 ready → drain 旧副本 → 等 in-flight 完成/超时 → 停旧副本。
- **共享存储**：契约要求所有副本 `paths.data_root` 指向同一共享文件系统；readiness 写入 `data_root/.probe-<sha256(instance_key)[:16]>` 并删除验证（避免路径非法字符）；不同路径视为不可用（readiness 503，日志告警）。单机演练用同一目录的两个进程作为“等价物”。
- **执行器**：pull 多端点（`execution.executor_urls`，逗号分隔，空=沿用 `execution.executor_url`）；push `POST /api/internal/executors/register|heartbeat`（Bearer 同一 `execution.executor_token`）；容量/排空/离线/重派沿用既有实现，补多执行器选择（active 且 active_contexts<capacity，按容量降序）。
- **不批量中断**：启动恢复只处理过期 lease（既有行为，`agent/application/worker.go:144-157`、`execution/application/recovery.go:135-160`）；`api_instance` 启动不得修改其它实例的 running 状态。

### 4.6 问题（reopen/merge/复发重定向/as-of）

- 重开/合并语义保持 P4（`issue_service.go:263-354`），不改终态（merged terminal）。
- **复发重定向**：`CreateCandidate` 命中 `(version_id,dedupe_key)` 且源 `status=merged` 时，沿 `merged_into_id` 链（≤16）找到目标 issue：
  - 向目标追加 refs（`role='recurrence'`，同一 `(kind,ref_id)` 已存在则跳过）；
  - `recurrence_count++`、`last_recurred_at=now`、状态历史（`from=to=<目标状态>`，reason=`recurrence`，actor=`system`）；
  - 事件 `issue.recurred`（`issue_id=目标`，`source_issue_id=源`，`dedupe_key`）；
  - 审计 `delivery.issue.recurred`；
  - 不回写源 issue 的 refs（保留历史归属）。
  - 链断裂/成环 → 记录错误事件（`issue.recurred` 带 `redirect_failed=true`）并保持旧行为（追加到源），同时写审计，避免丢数据。
- **旧报告 as-of**：publish 后 HTML 字节不变（既有）；新增测试证明合并/复发后旧 revision HTML 与 as_of 不变，新 revision 反映当前合并状态。
- **跨波次去重**：键已跨波次稳定（`issue_candidate.go:103-113`）；补验收断言（不同 wave 相同失败 → 同一 issue + refs/复发）。
- UI：合并链（源→目标跳转）、复发计数、重开（已有）、合并确认（已有）。

---

## 5. 数据模型（迁移 #34–#39）

> 约定：雪花 `BIGINT` 主键（应用生成）、`TIMESTAMPTZ`、`created_at/updated_at`、索引名 `pk_/uk_/idx_`（`db-schema.md:11-18`）。跨上下文只做逻辑引用。

### #34 `knowledge_0001_knowledge.sql`（新上下文 `knowledge`）

```sql
CREATE TABLE knowledge_item (
  id BIGINT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('global','system')),
  system_key TEXT NULL CHECK (system_key IS NULL OR (system_key = lower(system_key) AND char_length(system_key) BETWEEN 1 AND 64)),
  title TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
  content TEXT NOT NULL CHECK (char_length(content) BETWEEN 1 AND 20000),
  tags TEXT[] NOT NULL DEFAULT '{}'::text[] CHECK (cardinality(tags) <= 20),
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','rejected','revoked')),
  confidence NUMERIC(4,3) NULL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  source_version_id BIGINT NULL,
  promoted_from_version_id BIGINT NULL,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  review_reason TEXT NULL CHECK (review_reason IS NULL OR char_length(review_reason) <= 2000),
  reviewed_by BIGINT NULL, reviewed_at TIMESTAMPTZ NULL, revoked_at TIMESTAMPTZ NULL,
  created_by BIGINT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  search_vector tsvector GENERATED ALWAYS AS (
    to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(content,'') || ' ' || array_to_string(tags,' '))
  ) STORED,
  CONSTRAINT pk_knowledge_item PRIMARY KEY (id),
  CONSTRAINT ck_knowledge_item_scope CHECK ((scope = 'global' AND system_key IS NULL) OR (scope = 'system' AND system_key IS NOT NULL))
);
CREATE INDEX idx_knowledge_item_scope_status_id ON knowledge_item (scope, status, id DESC);
CREATE INDEX idx_knowledge_item_status_id ON knowledge_item (status, id DESC);
CREATE INDEX idx_knowledge_item_source_version ON knowledge_item (source_version_id) WHERE source_version_id IS NOT NULL;
CREATE INDEX idx_knowledge_item_search ON knowledge_item USING GIN (search_vector);

CREATE TABLE knowledge_revision ( ... id, knowledge_id FK CASCADE, revision, title, content, tags, changed_by, created_at, updated_at, UNIQUE(knowledge_id,revision) );
CREATE TABLE knowledge_usage ( ... id, knowledge_id FK, agent_run_id BIGINT NULL, work_item_id BIGINT NULL, token_estimate INTEGER, injected_at TIMESTAMPTZ, created_at, updated_at );
```

### #35 `storage_0003_gc.sql`

```sql
CREATE TABLE gc_run (id, mode CHECK(dry_run|apply), status CHECK(running|completed|failed),
  actor_id BIGINT NULL, actor_kind CHECK(user|system), lease_owner TEXT NULL, lease_until TIMESTAMPTZ NULL,
  lease_epoch BIGINT NOT NULL DEFAULT 0, started_at, finished_at, scanned_objects INT, scanned_refs INT,
  deleted_objects INT, deleted_refs INT, deleted_bytes BIGINT, report JSONB NOT NULL DEFAULT '{}'::jsonb,
  error TEXT NULL, created_at, updated_at);
CREATE UNIQUE INDEX uk_gc_run_running ON gc_run ((true)) WHERE status = 'running';  -- 单活跃运行
CREATE TABLE gc_tombstone (id, run_id FK, object_sha TEXT CHECK 64 hex, size_bytes, mime, relative_path,
  reason CHECK(unreferenced|temp|superseded_export|orphan_file), deleted_at, created_at);
CREATE INDEX idx_content_ref_live_object ON content_ref (object_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_content_object_created_at ON content_object (created_at);
```

### #36 `delivery_0004_report_pdf.sql`

```sql
ALTER TABLE delivery_report_revision
  ADD COLUMN pdf_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (pdf_status IN ('pending','running','done','failed','disabled')),
  ADD COLUMN pdf_error TEXT NULL CHECK (pdf_error IS NULL OR char_length(pdf_error) <= 2000),
  ADD COLUMN pdf_attempts INTEGER NOT NULL DEFAULT 0 CHECK (pdf_attempts >= 0),
  ADD COLUMN pdf_rendered_at TIMESTAMPTZ NULL;
CREATE INDEX idx_delivery_report_revision_pdf_pending ON delivery_report_revision (pdf_status) WHERE pdf_status IN ('pending','failed');
```
不变量：发布后仅 `pdf_ref/pdf_status/pdf_error/pdf_attempts/pdf_rendered_at/updated_at` 可变；`as_of/html_ref/input_fingerprint/template_version` 永远不变。既有已发布 revision 落为 `pending` 并进入 PDF 补齐队列（见 §4.2）。

### #37 `orchestration_0001_instance.sql`（新上下文 `orchestration`）

```sql
CREATE TABLE api_instance (id BIGINT PK, instance_key TEXT NOT NULL UNIQUE, hostname TEXT NOT NULL,
  pid INTEGER NOT NULL CHECK(pid > 0), api_port INTEGER NOT NULL CHECK(api_port BETWEEN 1 AND 65535),
  node_id INTEGER NOT NULL UNIQUE CHECK (node_id BETWEEN 0 AND 1023),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','draining','offline','stopped')),
  version TEXT NOT NULL DEFAULT '', last_error TEXT NULL,
  started_at TIMESTAMPTZ NOT NULL, heartbeat_at TIMESTAMPTZ NOT NULL,
  draining_at TIMESTAMPTZ NULL, stopped_at TIMESTAMPTZ NULL, created_at, updated_at);
CREATE INDEX idx_api_instance_status_heartbeat ON api_instance (status, heartbeat_at DESC);
```

### #38 `resource_0002_sut_family.sql`

```sql
ALTER TABLE sut ADD COLUMN family TEXT NULL CHECK (family IS NULL OR (family = lower(family) AND char_length(family) BETWEEN 1 AND 64));
CREATE INDEX idx_sut_family ON sut (family) WHERE family IS NOT NULL;
```

### #39 `delivery_0005_issue_recurrence.sql`

```sql
ALTER TABLE delivery_issue
  ADD COLUMN recurrence_count INTEGER NOT NULL DEFAULT 0 CHECK (recurrence_count >= 0),
  ADD COLUMN last_recurred_at TIMESTAMPTZ NULL;
```

---

## 6. API 契约（新增/变更）

> 全部沿用统一错误信封、`X-Request-ID`、Bearer；ID 为十进制字符串。`admin` = 管理员；“auth” = 登录用户。

### 6.1 知识（tag `Knowledge`）

| 方法/路径 | 权限 | 语义 |
|---|---|---|
| `GET /api/knowledge/items?scope=&status=&system_key=&tag=&q=&cursor=&limit=` | auth | 列表/全文检索（`ts_rank` + id 游标） |
| `POST /api/knowledge/items` | auth | 创建 draft（title/content/scope/system_key/tags/source_version_id/promoted_from_version_id） |
| `GET /api/knowledge/items/{knowledgeId}` | auth | 详情 + 最近 revisions + 是否 `source_stale` |
| `PATCH /api/knowledge/items/{knowledgeId}` | 作者或 admin，且 status∈draft/rejected | 编辑草稿（写 knowledge_revision，revision+1，rejected→draft） |
| `POST .../{id}/approve` | admin | draft/revoked → approved（`review_required=false` 时也允许 admin 一步） |
| `POST .../{id}/reject` | admin | draft → rejected（reason 必填） |
| `POST .../{id}/revoke` | admin | approved → revoked（reason 必填） |
| `GET .../{id}/revisions` | auth | 追加式内容历史 |

错误：404 `not_found`；409 `state_conflict`（非法状态迁移）；400 `invalid_argument`（scope/system_key 组合、长度、标签）。

### 6.2 GC（tag `Admin`）

| 方法/路径 | 权限 | 语义 |
|---|---|---|
| `POST /api/admin/gc/runs` | admin | body `{mode: "dry_run"\|"apply", confirm?: bool}`；apply 需 `confirm=true`（否则 400 `confirmation_required`）；返回 run（含 202 语义） |
| `GET /api/admin/gc/runs?limit=` | admin | 运行历史 |
| `GET /api/admin/gc/runs/{gcRunId}` | admin | 详情 + 候选/删除明细（report JSONB）+ tombstones |

### 6.3 实例/编排（tag `Admin`）

| 方法/路径 | 权限 | 语义 |
|---|---|---|
| `GET /api/admin/instances` | admin | 实例列表（含 node_id/status/heartbeat/pid/port/version） |
| `POST /api/admin/instances/{instanceId}/drain` | admin | 排空（readiness 失败、停止认领；in-flight 完成） |
| `POST /api/admin/instances/{instanceId}/activate` | admin | 取消排空 |
| `GET /api/health/ready` | 公开 | DB ping + 存储写探针 + 非 draining；200/503 |

### 6.4 报告 PDF（tag `Delivery`）

| 方法/路径 | 权限 | 语义 |
|---|---|---|
| `GET /api/versions/{versionId}/reports/{reportId}/revisions/{revision}/pdf` | auth（版本可读同上） | 200 `application/pdf` + ETag；404 未生成/不存在；409 `pdf_not_ready`（pending/running）、`pdf_failed`（failed） |

`ReportRevisionView` 变更（非破坏性扩宽）：`pdf_available` 改 `boolean`；新增 `pdf_status`、`pdf_error`（可空）、`pdf_rendered_at`（可空）。
`IssueView` 新增 `recurrence_count`、`last_recurred_at`（可空）、`merged_chain`（目标链 ID 数组，可选）。
`SutView`/SUT 写模型新增可选 `family`。

### 6.5 内部（tag `Internal`，Bearer token，非公开）

| 方法/路径 | 语义 |
|---|---|
| `POST /api/internal/executors/register` | 执行器启动注册：endpoint/name/capacity/version/max_contexts |
| `POST /api/internal/executors/heartbeat` | 心跳：active_contexts/capacity/version/status(degraded) |

---

## 7. 事件与审计（全部 `frozen: P7`）

| 事件 | 频道 | payload | 触发 |
|---|---|---|---|
| `report.pdf_ready` | `version:{id}` | `report_id, report_revision_id, revision, pdf_ref, size_bytes` | PDF 写入成功 |
| `report.pdf_failed` | `version:{id}` | `report_id, report_revision_id, revision, attempts, error` | 超限失败 |
| `knowledge.proposed` | `system` | `knowledge_id, scope, source_version_id?` | 创建 draft |
| `knowledge.approved` | `system` | `knowledge_id, scope, reviewer_id` | approve |
| `knowledge.rejected` | `system` | `knowledge_id, reason` | reject |
| `knowledge.revoked` | `system` | `knowledge_id, reason` | revoke |
| `gc.completed` | `system` | `gc_run_id, mode, deleted_objects, deleted_bytes, run_status` | GC 结束 |
| `instance.registered` | `system` | `instance_id, node_id, instance_key` | 启动注册 |
| `instance.drained` / `instance.activated` / `instance.offline` | `system` | `instance_id, status` | 状态迁移 |
| `issue.recurred` | `version:{id}` | `issue_id, source_issue_id, dedupe_key, recurrence_count, redirect_failed?` | 复发重定向 |

审计 action：`delivery.report.pdf_rendered`、`delivery.report.pdf_failed`、`knowledge.*`（create/edit/approve/reject/revoke）、`storage.gc.completed`、`orchestration.instance.drained/activated`、`delivery.issue.recurred`。

---

## 8. 设置键（72 → 83，全部 DB 设置中心）

| Key | 类型/默认 | 说明 |
|---|---|---|
| `report.template_version` | string `"2"`（原 `"1"`；新库默认 2，既有库保留原值） | 内置模板版本，仅允许 1/2 |
| `report.section_appendix` | bool `true` | v2 附录章节开关 |
| `report.render_max_attempts` | number `3`（1–10） | PDF 任务重试上限 |
| `report.render_lease_seconds` | number `120`（30–1800） | PDF 任务租约 |
| `report.render_worker_url` | string `http://127.0.0.1:13337` | renderer 地址 |
| `report.render_token` | string secret `eztest-dev-render-token` | renderer Bearer |
| `knowledge.injection_max_items` | number `5`（0–50） | 注入条数上限 |
| `retention.gc_interval_minutes` | number `1440`（5–10080） | 周期 GC |
| `retention.gc_batch_size` | number `500`（1–5000） | 单轮扫描上限 |
| `instance.heartbeat_seconds` | number `10`（1–60） | 实例心跳 |
| `instance.offline_seconds` | number `30`（5–300） | 实例离线阈值 |
| `execution.executor_urls` | string `""` | 额外执行器端点（逗号分隔；空=用 `execution.executor_url`） |

合计 72 → **83**（新增 11）。已存在但 P7 首次消费：`report.render_pdf`、`report.template_version`、`report.section_*`、`knowledge.injection_budget_tokens`、`knowledge.review_required`、`retention.temp_hours`、`retention.replaced_export_days`、`retention.unreferenced_content_days`、`retention.gc_grace_hours`、`retention.gc_dry_run`、`paths.data_root`、`execution.executor_url`、`execution.executor_token`、`execution.executor_heartbeat_seconds`、`execution.executor_offline_seconds`、`runtime.snowflake_node_id`。

---

## 9. 任务、工具与 profile

- **任务类型**：注册 `report.render_pdf`（`team=delivery`，版本域，payload `{report_id, report_revision_id}`，dedupe `report:{reportID}:rev:{revision}`，max_attempts 取 `report.render_max_attempts`）。
- **工具**：新增 `knowledge.propose`（澄清/设计 profile 白名单；参数 title/content/tags/scope?/system_key?，服务端强制 draft + 当前版本为 source）；`knowledge` 注入进入 `version_pack`。
- **Profile 白名单增量**：clarification Host/Scout、design Host/Planner/Author 允许 `knowledge.propose`；读取注入无需工具。
- **worker 装配**：`cmd/eztest` 新增 `RenderWorker`、`GCWorker`、`InstanceRegistry`/`Leader`、`SSE listener`；leader 门控 reconciler/scheduler/poller/GC。
- **work_item 认领扩展**：`ClaimForTypes(ctx, claimant, types, lease)`（`FOR UPDATE SKIP LOCKED`，只认领 `open` 或过期 `claimed/running`），供 render worker 使用；agent worker 行为不变。

---

## 10. 前端交付

| 页面/组件 | 变更 |
|---|---|
| `/knowledge`（`frontend/src/app/(app)/knowledge/page.tsx` → `features/knowledge/`） | 列表（scope/status/标签/全文检索/游标）、详情、创建/编辑草案、admin 审核（approve/reject/revoke/re-approve）、revisions、来源版本与 `source_stale` 标识；复用现有 UI 原语与 api-client |
| 报告 Tab（`features/delivery/ReportDetailPanel.tsx`、`ReportsTab.tsx`） | PDF 状态徽标（pending/running/done/failed/disabled）、下载、失败原因、`report.render_pdf` 关闭提示；附录章节由服务端 HTML 呈现；模板版本显示 |
| 设置页（`features/settings/SystemSettings.tsx`） | “数据保留与 GC”面板：dry-run 运行、报告明细、历史、apply 二次确认；“实例”面板：列表/状态/drain/activate（admin） |
| 问题 Tab（`features/delivery/IssuesTab.tsx`、`IssueDetailSheet.tsx`） | 复发计数、合并链（源→目标）、`issue.recurred` 提示；重开/合并沿用 |
| 资源页（`features/resources/`） | SUT 增加可选 `family` 编辑与展示 |
| 导航（`src/navigation/nav-items.ts`） | 知识页去掉 placeholder 标记 |

约束：所有请求经 `src/lib/api-client.ts`；SSE 经 `src/lib/sse-client.ts`；admin 面板只在管理员可见（复用现有身份判定）。

---

## 11. 配置与部署

- 保持 `.env` 严格 4 项（`decision-log.md:127`）；renderer/GC/实例参数全部走 DB 设置。
- `renderer/`：`package.json`（playwright 1.64.1）、`src/server.mjs`、`test/*.test.mjs`；环境变量 `EZTEST_RENDERER_ADDR`（默认 `127.0.0.1:13337`）、`EZTEST_RENDER_TOKEN`、`EZTEST_RENDER_TIMEOUT_SECONDS`（默认 60）；使用已安装 Chrome channel（与前端 Playwright 配置一致，`frontend/playwright.config.ts:31`）；`scripts/dev-renderer.ps1` 从 DB 读 `report.render_token`（与 `dev-executor.ps1` 同模式）。
- `deploy/`：`Dockerfile.renderer`（官方 Playwright 镜像）；`docker-compose.yml` 增加 `renderer` 服务与 **两个 api 副本**（共享同一 `EZTEST_DATA` 卷、同一 PG）；清理 0.1 env；健康检查指向 `/api/health/ready`。
- `scripts/p7-multi.ps1`：本机双 API 进程（不同 `EZTEST_API_PORT`、同 DB、同 `paths.data_root`、不同 `runtime.snowflake_node_id` 允许自动分配）+ 断言。
- 文档：handoff 记录生产需 NFS/等价物；单机演练为等价验证。

---

## 12. 测试与验收

### 12.1 测试层

- **单测**（每个新包）：知识状态机/scope 校验/注入预算排序；GC 候选分类/保护/宽限/保留期；实例 node 分配/leader 门控/readiness；PDF 任务 ensure/重试/元数据 fence；复发链重定向。
- **集成**（`-tags=integration`）：迁移 #34–#39 幂等与表/索引；知识 FTS/游标；GC apply 的 ref→object→文件顺序与 tombstone/审计；report PDF 元数据不可变性；issue recurrence 事务；`api_instance` 唯一 node_id 分配。
- **契约**：`scripts/p7-contract-check.cjs`（新路径/事件/表/迁移/设置/任务/工具计数）+ P2–P6 前向兼容门禁。
- **多副本演练**：`scripts/p7-multi.ps1`（双进程 + 真实 PG）。
- **真实 LLM + 真实 Chrome 验收**：`scripts/p7-acceptance.ps1`。
- **浏览器 E2E**：`scripts/p7-e2e.ps1`（`frontend/e2e/p7.spec.ts`）。

### 12.2 验收场景（S0–S12，真实环境）

- 环境：`eztest_v02_p6`（33 迁移/2 版本/5 波次/2 问题/7 报告/9 revision/53 run/72 设置）克隆为 `eztest_v02_p7`；真实 DeepSeek；真实 Chrome；本机双 API 副本 + renderer。
- S0 启动：39 迁移、83 设置、renderer health、`/api/health/ready` 200。
- S1 报告 PDF：选已发布 revision → 生成 PDF（`%PDF-` + 大小 + sha）→ 下载 200；HTML/as_of 不变；`pdf_available=true`。
- S2 PDF 重试：停 renderer → 触发新 revision PDF → failed 状态 + 事件 → 启 renderer → 自动重试成功。
- S3 模板/章节：`report.template_version=2` + 附录开关 on/off；HTML 含/不含附录；旧 revision 仍为 1 且不变。
- S4 知识生命周期：创建 draft（version 来源）→ admin approve → 注入到澄清/设计 context packet（`knowledge_refs` + `[K-]` 引用 + usage 行）→ revoke → 不再注入 → 复审 approve 恢复。
- S5 知识 scope：global 对所有版本可见；system 仅匹配 `sut.family` 的版本；标签/全文检索命中；预算上限生效。
- S6 GC dry-run：构造孤儿对象/临时文件/被替代导出/无引用内容/受保护对象 → dry-run 报告分类正确、无删除、无 tombstone。
- S7 GC apply：`confirm=true` → 仅候选删除，受保护对象与旧 revision PDF/HTML 保留；tombstone 行、审计、`gc.completed` 事件齐全；`confirm` 缺失 400。
- S8 跨实例 SSE：实例 A 发布事件，实例 B 的 SSE 订阅者收到；`Last-Event-ID` 回放一致。
- S9 副本/排空/滚动：起副本 B → ready；drain A → readiness 503、A 不再认领；A 停止后 B 接管 leader；无重复执行（task/agent_run/case_run 计数断言）；迁移只执行一次。
- S10 执行器：多端点/push 注册可见；停执行器 → offline → running case_run `interrupted(executor_lost)` + 重派；不批量中断其它 run。
- S11 问题复发：合并 A→B；同 dedupe_key 新失败 → 重定向到 B（refs/recurrence_count/事件/审计），A 不变；跨波次不新建 issue。
- S12 旧报告 as-of：合并/复发后旧 revision HTML 字节与 as_of 不变；新 revision 反映当前状态；收尾谓词仍满足。
- 终局：`scripts/test.ps1`、`node scripts/p7-contract-check.cjs`、P2–P6 门禁、`scripts/p7-e2e.ps1`（截图 8+ 张到 `acceptance/p7/screenshots/`）。

### 12.3 验收清单（映射 phase-07）

- [ ] 报告 HTML/PDF、模板/章节、revision/as-of/stale
- [ ] 知识晋升/审核/注入/撤回；GC dry-run/引用保护/保留期
- [ ] 多副本：加副本/滚动升级/故障恢复不重复执行/不批量中断；SSE 跨实例；执行器离线重派
- [ ] 问题重开/合并/去重/旧报告 as-of
- [ ] 测试/演练全绿；contracts frozen

---

## 13. 契约冻结与文档变更

- `openapi.yaml`：`0.2.0-p7`、`x-frozen: P7`；新增 16 个 operation（知识 8、GC 3、实例 3、health ready 1、PDF 下载 1）+ 内部 2 个；`ReportRevisionView.pdf_available` 扩宽 + PDF 字段；`IssueView` 复发字段；SUT `family` 可选。
- `events.md`：§16（12 个新事件，见 §7）。
- `db-schema.md`：§16（6 张新表 + 3 张既有表列扩展：`delivery_report_revision`、`delivery_issue`、`sut`），并更新 P4 `pdf_ref` 注记为 P7。
- `migrations-order.md`：§9（#34–#39，`order.txt` 39 条）。
- `settings.md`：§4g（72→83；`report.template_version` 默认值说明）。
- `task-types.md`：§8（`report.render_pdf`）。
- `agent-profiles-tools.md`：§10（`knowledge.propose`、注入字段、白名单）。
- `executor-protocol.md`：§12（push 注册/心跳、多端点、容量选择）。
- `contracts/README.md`：P7 冻结状态与计数。
- 不修改设计文档（`architecture-design.md`/`high-level-design.md`/`research/*`/`ui/*`）。

---

## 14. 风险与回滚

| 风险 | 对策 |
|---|---|
| GC 误删 | 默认 dry-run；宽限 24h；引用保护；tombstone+审计；apply 二次确认；删除顺序 ref→row→file；测试覆盖受保护对象 |
| PDF 渲染拖垮 API / Chrome 不稳定 | 独立进程；超时；重试上限；失败不阻塞 HTML/收尾；renderer health 参与 readiness 告警（不参与 API ready 硬门禁） |
| 多副本重复执行 | leader advisory lock + 既有 lease/fencing；演练断言计数；启动只恢复过期 lease |
| 雪花 node 回收导致碰撞 | 仅回收 offline/stopped 实例；回收前二次确认无心跳；单机演练用不同端口/手动 node |
| 共享存储不一致 | readiness 写探针；同一 `paths.data_root` 契约；不一致时 503 |
| 知识污染 | 默认 `review_required=true`；AI 只能 draft；approve/revoke 仅 admin；来源版本/审计可追溯 |
| 模板默认值变化影响既有库 | 仅新库默认 2；既有库保持 1，可显式切换；旧 revision 不重渲染 |
| 0.1 harness/CI 静态门禁仍红 | 记录 handoff；P7 只保证 `scripts/test.ps1` + P7/P2–P6 门禁；P8 处置 |
| 回滚 | 迁移只追加；功能开关：`report.render_pdf=false`、GC 只 dry-run、知识可 revoke、实例可 drain；代码回滚按分支 |

---

## 15. Backlog（P8 / 0.3，不实现）

- 事件日志/审计日志保留与清理；限流（per-instance/DB 配额）。
- 报告 diff；自定义模板编辑器；多模板。
- 知识：`knowledge.search` 工具、向量检索、跨系统冲突合并建议、按系统 owner 审核。
- 问题：撤销合并、merged 重开、跨版本合并、AI 合并建议一键应用。
- 执行器：remote browser/VNC、storage_state 复用、`wait_until`/超长等待；push 全量切换。
- 0.1 数据只读归档/导出脚本。
- 0.1 harness 清理与 CI 静态门禁切换到 0.2。
