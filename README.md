# EzTest

EzTest 的 P0 基础阶段（foundation）、P1 Agent 运行时、P2 公共资源/身份、P3 版本/多需求/澄清、P4 代码理解/影响分析、P5 用例脑图、P6 用例执行、P7 全自主闭环/交付与 P8 体验完善/硬化实现：Go 后端 API、Next.js 管理前端、Playwright 执行器、PostgreSQL 持久化，以及 SSE 事件流、对象存储、审计、LLM 配置管理、Agent 控制台、公共资源管理、版本工作台与澄清、代码影响分析、用例脑图、真实浏览器执行/证据/暂停恢复、AI 补全与失败分析、问题/报告、DB 断言、继续中增量批次、探索链路 v2、全局搜索/待办、知识沉淀、评测/质量中心、限流/SLO、证据 GC、备份恢复与多实例单活。

- 分支：`dev`（P8 硬化阶段；HEAD 见 `git log`）
- P8 阶段规格：[`docs/superpowers/specs/2026-10-29-eztest-p8-hardening-design.md`](docs/superpowers/specs/2026-10-29-eztest-p8-hardening-design.md)
- P8 发布说明：[`docs/release-notes/p8.md`](docs/release-notes/p8.md)
- 部署/运维/用户/开发文档：[`docs/deployment.md`](docs/deployment.md)、[`docs/operations.md`](docs/operations.md)、[`docs/user-guide.md`](docs/user-guide.md)、[`docs/development.md`](docs/development.md)
- P7 阶段规格：[`docs/superpowers/specs/2026-10-28-eztest-p7-autonomous-loop-design.md`](docs/superpowers/specs/2026-10-28-eztest-p7-autonomous-loop-design.md)
- P6 阶段规格：[`docs/superpowers/specs/2026-10-28-eztest-p6-execution-design.md`](docs/superpowers/specs/2026-10-28-eztest-p6-execution-design.md)
- P5 阶段规格：[`docs/superpowers/specs/2026-10-28-eztest-p5-mindmap-review-design.md`](docs/superpowers/specs/2026-10-28-eztest-p5-mindmap-review-design.md)
- P4 阶段规格：[`docs/superpowers/specs/2026-10-28-eztest-p4-code-intelligence-design.md`](docs/superpowers/specs/2026-10-28-eztest-p4-code-intelligence-design.md)
- P3 阶段规格：[`docs/superpowers/specs/2026-10-27-eztest-p3-version-requirements-design.md`](docs/superpowers/specs/2026-10-27-eztest-p3-version-requirements-design.md)
- P2 阶段规格：[`docs/superpowers/specs/2026-10-27-eztest-p2-resources-identity-design.md`](docs/superpowers/specs/2026-10-27-eztest-p2-resources-identity-design.md)
- P1 阶段规格：[`docs/superpowers/specs/2026-10-27-eztest-p1-agent-runtime-design.md`](docs/superpowers/specs/2026-10-27-eztest-p1-agent-runtime-design.md)
- P0 阶段规格：[`docs/superpowers/specs/2026-10-27-eztest-p0-foundation-design.md`](docs/superpowers/specs/2026-10-27-eztest-p0-foundation-design.md)
- OpenAPI：[`docs/openapi/api.yaml`](docs/openapi/api.yaml)（公开端点 `GET /api/openapi.yaml`）
- 设计权威：[`docs/design/system-blueprint.md`](docs/design/system-blueprint.md)；实现约定见 [`Agent.md`](Agent.md)

## P0 范围

已实现：

- 账号与认证：单管理员种子、登录/登出/当前用户、Bearer token（只存 SHA-256 哈希）、登录失败限流。
- 用户管理：管理员创建普通用户、列表/分页、启用/禁用、重置密码（撤销该用户全部 token）。
- LLM 配置管理：CRUD、AES-256-GCM 加密存储 Key/headers、模型目录刷新、非流式/流式连通性测试（含 1×1 PNG 多模态）。
- 事件流：`system` 频道事件快照、SSE 实时流、断线按 `Last-Event-ID` 回放、`stream.reset`、20s 心跳、管理端测试事件发布。
- 对象存储：内容寻址（SHA-256）、本地文件系统、10 MiB 上限、下载时完整性校验。
- 审计：管理员查询、登录/用户/LLM/存储/测试事件动作记录。
- 统一约定：`{"data": ...}` 成功信封、统一错误信封与错误码、`X-Request-ID`、幂等键、CORS、健康检查、OpenAPI。
- 本地开发脚本、E2E、P0–P8 验收入口；Docker/Compose 交付文件与静态校验。

P0 **不包含**版本/需求/用例/执行/证据/问题/报告等业务实体与页面，也不包含代码仓库管理、多实例/多租户等能力（详见 P0 阶段规格 §16）。Agent 运行时不在 P0 内，由 P1 在 P0 之上补齐。

## P1 Agent 运行时

P1 在 P0 的事件流、LLM 网关与权限模型之上增加一个可用的 Agent 运行时；**所有 `/api/agents/*` 端点、`agent:` 事件通道与 `/agents` 页面均为管理员专用**。

- 控制台：管理员登录后打开 `/agents`。左侧会话列表与新建会话，中间 transcript（thinking 折叠、工具调用折叠/展开、运行状态、停止按钮），右侧配置页可修改系统提示词/模型/工具白名单等；活动事件实时显示，刷新后按快照 + SSE 回放。
- API（12 个端点）：`GET /api/agents/profiles`、`GET/PATCH /api/agents/profiles/{id}`、`GET /api/agents/tools`、`GET/POST /api/agents/sessions`、`GET/PATCH/DELETE /api/agents/sessions/{id}`、`POST /api/agents/sessions/{id}/messages`、`POST /api/agents/sessions/{id}/stop`、`GET /api/agents/sessions/{id}/messages/{message_id}`。写操作沿用 `Idempotency-Key`，错误沿用 P0 错误信封。
- 运行时：内置 `runtime-diagnostic` profile（可编辑、无删除端点）；每个 run 有 `running/done/failed/aborted/interrupted` 状态与 `max_turns`、`run_seconds`、`hard_timeout_seconds` 预算；同一 session 同时最多一个 run，全局并发默认 4；停止会取消流式/工具并保留已生成内容。
- 工具：`test.echo`、`test.fail` 始终注册；`file.list`/`file.read` 仅在 `EZTEST_AGENT_FILE_ROOT` 非空时注册（空值 = 不启用，默认）；`http.get` 只允许 `EZTEST_AGENT_HTTP_ALLOWLIST` 中的 host（默认 `127.0.0.1,localhost`）。工具调用、结果、错误与中止均写审计。
- 事件契约 V1（冻结）：`type=agent.activity`、`channel=agent:{session_id}`、`agent_role=profile.key`、`session_id` 为字符串化会话 ID；`detail.kind` 共 9 类：`run_started`、`user_message`、`thinking`、`text`、`tool_use`、`tool_result`、`usage`、`decision`、`run_finished`。thinking/text 以 1500ms 或 1024 字符分片（`final` 标记），按 `(message_id, part)` 拼接；事件只带预览，完整内容用 message detail 端点懒加载。
- 新增 4 个环境变量：`EZTEST_AGENT_FILE_ROOT`（空 = 禁用文件工具）、`EZTEST_AGENT_HTTP_ALLOWLIST`、`EZTEST_AGENT_TOOL_TIMEOUT_MS`（默认 10000）、`EZTEST_AGENT_MAX_CONCURRENT_RUNS`（默认 4），详见下方环境变量表。
- 本地启动：先按「本地启动」运行 API/Web，管理员登录后进入 `/agents`。要让文件工具可用，设置 `EZTEST_AGENT_FILE_ROOT=<仓库根绝对路径>` 并重启 API（`scripts/dev-api.ps1` 在未设置时默认使用仓库根，`.env` 显式设置优先）；内置 profile 的 `allowed_tools` 已列出文件工具，未设置时运行时会拒绝调用。通常无需重新保存 profile，只有曾在禁用状态下把文件工具从白名单移除过，才需要在启用后重新保存。
- 测试：Agent 单元/集成套件 `go test ./internal/agent/...` 与 `go test -tags=integration ./internal/agent/...`；`scripts/e2e.ps1` 会启动 fake 上游 `scripts/fake-llm.cjs`（默认 13334）并覆盖 `llm_*`，P0+P1 E2E 不产生真实费用。真实 `deepseek-flash` 的思考 + 工具验收是 gated（见下文「真实 LLM（gated）」）。

## P2 公共资源与身份

P2 增加 `resource` 限界上下文与公共资源页面；`/api/resources/*` 的所有 GET 对任意已登录用户开放，所有写操作与动作仅管理员可用（普通用户只读，写 API 返回 403）。

- 页面：`/resources` 四个 Tab —— 系统与环境、应用模型、账号与访问、会话；支持 `?tab=systems&system=<id>`、`?tab=sessions&session=<id>` 深链。`/code-repos` 管理本地代码仓（创建/编辑/删除/重新校验），显示 `localPath`、`fixedBranch` 与 `lastCommit`；代码索引与影响分析由 P4 `codeintel` 提供（见下文）。
- Session：从 Access 导入 Playwright `storageState` 或 token `headers`；状态为 `pending`/`acquiring`/`needs_manual`/`valid`/`refreshing`/`expired`/`invalid`/`revoked`/`failed`（`expiring` 为派生标记）。动作为导入 → validate/confirm（login_check 不可用时人工确认，快照标记 degraded）→ invalidate/revoke，以及人工接管（发起/完成/取消）。API 只暴露摘要（`storageStateHash`、cookie/origin 计数），不返回凭据或 `storageState` 原文。
- 凭据与状态（D28）：Access 的 `credential` 与 Session 的 `storageState` **明文存储**，P2 不实现加密、脱敏与审计日志；API 永不回显原文，只返回 `credentialSet` 与 storageState 的 hash/形状统计。P2 不新增 EzTest 环境变量（fixture 的 `FIXTURE_PORT` 仅测试脚本使用）。
- fixture：`node scripts/fixture-target.cjs`（默认 `127.0.0.1:13335`，可用 `FIXTURE_PORT` 覆盖）提供 `/login`（`fixture` / `fixture-pass`）、`/account`、`/api/profile`（`Authorization: Bearer fixture-token`）与 `/health`，用于导入真实 storageState / token 的验收；`scripts/e2e.ps1` 会自动启动并停止它。
- 验收命令：

```powershell
# 快速门禁（Go 单元 + 集成 + 前端 lint/typecheck/test）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1
# 资源域细粒度
cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/resource/...
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/resource/...
# E2E（含 fixture target）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1
# 完整 P0–P8 验收（含 OpenAPI 契约关键词检查）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1
```

## P3 版本 / 多需求 / 澄清

P3 增加 `version` 限界上下文（7 张表 + 附件文件系统 + 雪花 ID），交付「版本容器 + 多需求 + AI 引导澄清 + 人工确认」垂直切片。`/api/versions/*` 全部登录用户可读写，P3 不新增 403 权限点；写操作由 identity context 注入 actor 并记录 `createdBy`/`updatedBy`。

- 页面：`/versions` 列表/创建/资源绑定；`/versions/[id]` 工作台，默认「需求与澄清」Tab（功能工作台），另有「总览」骨架 Tab、「代码与变更」Tab（P4 实现）与执行会话、探索链路、实时播报、结果与证据、问题、报告 6 个占位 Tab（P5–P7 归属）。支持 `?tab=overview|sessions|exploration|broadcast|results|issues|report|code` 深链。
- 状态：需求独立流水线 `draft → clarified`，版本 `draft → clarifying → designing`。新增/确认/重开需求**不会**改变 Version.status；`designing` 下仍可独立澄清与确认；进入设计允许带未澄清需求，但需 `acknowledgeUnclarified=true` 二次确认并返回 warnings。
- 澄清：会话/消息（P1 `clarification-dialog`）、问题（open/answered/dismissed）、决策（proposed/accepted/rejected）与需求关系（`depends_on`/`conflicts_with`/`overlaps`/`shares_scope`）；SSE 固定 `channel=version:{id}`，复用 `Last-Event-ID` 回放、20s 心跳与 `stream.reset`；事件快照为扁平 `{"data":[...],"snapshot_cursor","has_more"}`，注册全部 P3 事件类型（含 `clarification.context_updated`）。
- ID：P3 自有 ID（version/requirement/relation/session/question/decision/attachment）在 JSON/URL 中一律为十进制字符串；P2 资源 ID（`systemId` 等）与用户 ID 仍为 number；前端禁止对 P3 ID 使用 `Number()`。
- 附件：仅文本（`.txt/.md/.json/.yaml/.yml`）与图片（`.png/.jpg/.jpeg`），单文件 ≤10MiB、每需求 ≤10、每版本 ≤50；文件落 `EZTEST_VERSION_ATTACHMENT_DIR`，DB 只存相对路径，下载需登录（`nosniff` + 安全 `Content-Disposition`）；context-recon 以 `image_ref` 注入，data URL 仅在 LLM 调用内存中生成、不落库。
- AI 降级：LLM/Agent 不可用时 `POST sessions/messages/analyze/recon` 返回 503 `ai_unavailable`（retryable），版本/需求/人工问题/决策/关系/确认路径不受影响。
- 新增环境变量：`EZTEST_VERSION_ATTACHMENT_DIR`（默认 `.data/version-attachments`）、`EZTEST_SNOWFLAKE_NODE_ID`（0–1023，默认 `1`；多实例必须唯一），详见下方环境变量表。
- 数据库：新增 `version_0001_versions_requirements.sql`、`version_0002_clarification.sql` 与 P1 兼容扩展 `agent_0002_activity_channel.sql`，启动时自动迁移；P3 不写 `audit_logs`，关键动作通过版本事件投影。

P3 验收命令：

```powershell
# P3 单元 + 集成（仓库根 → backend）
cd backend
..\.tools\go\bin\go.exe test -count=1 ./internal/version/...
$env:EZTEST_TEST_DATABASE_URL = "<eztest_test URL>"
..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/version/...

# P3 前端组件（backend → frontend）
cd ..\frontend
pnpm vitest run src/features/versions

# 仅 E2E（frontend → 仓库根；fake LLM，零真实费用）
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1

# 完整 P0–P8 验收（仓库根；P3/P4/P5/P7 关键词、错误 reason 与事件目录检查；真实 LLM 仍 gated）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1
```

P3 的 Playwright E2E（`frontend/e2e/versions*.spec.ts`，共 3 个 spec）已落地并纳入 `scripts/e2e.ps1`；T20 记录完整套件 22 passed（含 P3 3 个与 P4 5 个 spec），真实 LLM 人工验收仍为 gated。

真实 LLM 的 P3 人工验收为 gated：需 `.env` 配置有效 `llm_*`、显式设置 `EZTEST_ACCEPTANCE_REAL_LLM=1`，并人工执行创建版本/需求/附件 → ≥2 轮澄清 → 确认 → 进入设计；截图保存到 `.tmp/p3-acceptance/`（spec §10.7）。

## P4 代码理解与影响分析

P4 增加 `codeintel` 限界上下文（9 张表、雪花 ID、`repo_id`/`version_id`/`requirement_id`/`app_model_id` 为逻辑引用且不建跨上下文 FK），把 P3「代码与变更」占位 Tab 替换为可用功能：只读 Git 文件树/文件内容窗口/文件级 diff/单文件 unified diff/commit 元数据，T0 索引与 AI 事实、版本级影响分析与人工候选调整、需求/应用模型锚点。P4 不新增 Agent profile，不修改 P3 `code.tree`/`code.read` 契约。

- 仓库级 API（前缀 `/api/code/repos`）：`GET /api/code/repos`（仓库 + 最新索引状态）、`POST /api/code/repos/{repoId}/index`（管理员）、`GET .../index-runs/latest`、`GET .../index-runs/{runId}`、`GET .../tree`、`GET .../file`（审计读取）、`GET .../diff`、`GET .../diff/file`、`GET .../facts`、`GET .../search`、`GET .../commits/{sha}`。P4 的读接口对全部登录用户开放，仅索引触发要求管理员。
- 版本级 API（前缀 `/api/versions/{id}/code`）：`GET /changes`（只读预览，`changeSetId` 可为 null）、`POST /analyses`（202 新建 / 200 复用；未带 `force` 复用，`force=true` 产生 `analysisVersion+1`）、`GET /analyses/latest`、`GET /analyses/{analysisId}`、`PATCH .../candidates/{candidateId}`（`{selected}`）、`GET/POST /anchors`、`DELETE /anchors/{anchorId}`。P4 不新增 SSE 端点，`code.changed` 与 `code.impact_computed` 事件复用 `channel=version:{id}`。
- ID 规则：codeintel 自有 ID（analysisId/indexRunId/changeSetId/entityId/anchorId/candidateId）与 `subjectId` 为十进制字符串，`repoId`/`createdBy`/`selectionUpdatedBy` 保持 number；`factVersions[]` 是 `{factKey, version}` 快照，其中 `version` 是整数版本计数器，不是 ID；前端不得对字符串 ID 调用 `Number()`。
- 影响分析：T0 change set + 文件级 AI 缓存（命中不调 LLM）+ 事实版本化；候选来源 direct/contract/relation/ai，带 reason/confidence/evidence，low 默认不勾选，可人工 PATCH；未被事实/锚点覆盖的变更进入 `unmapped`。AI 不可用或部分失败时 `status=done, degraded=true`，保留 T0/直接锚点候选，不阻塞主流程。
- 代码仓只读挂载：P4 只读取 Git 对象与工作树，不写 checkout、不执行仓库代码。本地 `scripts/dev-api.ps1` 直接使用主机路径；部署时把代码仓目录以 **read-only** 方式挂载进 API 容器（例如 `-v D:\repos\app:/repos/app:ro`），P2 的 `localPath` 填容器内可见路径（`/repos/app`），并确保运行 API 的用户可读。仓库路径必须是 Git 工作树：非 Git 工作树返回 422 `path_not_git_repo`；路径不存在/不可读、Git 命令超时等运行期故障返回 503 `git_unavailable`（retryable）。
- 新增 8 个环境变量：`EZTEST_CODEINTEL_MAX_CHANGED_FILES`（默认 20）、`EZTEST_CODEINTEL_MAX_FILE_BYTES`（65536）、`EZTEST_CODEINTEL_MAX_INDEX_FILES`（5000）、`EZTEST_CODEINTEL_ANALYSIS_TIMEOUT_MS`（180000）、`EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES`（1）、`EZTEST_CODEINTEL_PROMPT_VERSION`（`p4.1`）、`EZTEST_CODEINTEL_MAX_EXCERPT_LINES`（40）、`EZTEST_CODEINTEL_MAX_DIFF_BYTES`（262144），详见下表。
- 数据库：`order.txt` 末尾追加 `codeintel_0001_code_facts.sql` 与 `codeintel_0002_code_impact.sql`，启动时自动迁移；迁移条数与 P4 表清单由验收脚本从 `order.txt` 及迁移 SQL 动态推导，不硬编码。

P4 验收命令：

```powershell
# P4 单元 + 集成（仓库根 → backend）
cd backend
..\.tools\go\bin\go.exe test -count=1 ./internal/codeintel/...
$env:EZTEST_TEST_DATABASE_URL = "<eztest_test URL>"
..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/codeintel/...

# P4 前端组件（backend → frontend）
cd ..\frontend
pnpm vitest run src/features/code-intel

# P4 fake-LLM 契约（仓库根；读取 .tmp/p4-e2e-repo，先运行 fixture 脚本）
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/fixture-codeintel-repo.ps1
node scripts/fake-llm.p4.test.cjs

# 仅 P4 静态门禁（OpenAPI/README/env/迁移 + fake-LLM，不需要数据库与 Playwright；
# 仍需 Node、frontend/node_modules/yaml 与 git）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly

# 完整 P0–P8 验收（P4/P5/P7 OpenAPI needle、reason、事件、fake-LLM 测试与 E2E 已纳入；真实 LLM 仍 gated）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1
```

P4 的真实 LLM 验收是 gated：未设置 `EZTEST_RUN_REAL_LLM=1` 时 `scripts/acceptance-codeintel-llm.ps1` 打印 `SKIPPED` 并退出 0。设置后脚本会先校验 `EZTEST_DATABASE_URL`（postgres 协议、显式 host 与有效 port（缺省按 5432），拒绝 `?dbname=`/`?database=`/`?host=`/`?port=` 等改目标参数，并确认 API 与 psql 指向同一个 `eztest_e2e`），再把来源仓库（默认当前 checkout；或 `EZTEST_ACCEPTANCE_P4_REPO` 指定的 Git 仓库）克隆到 `.tmp/p4-real-repo`（每次重建，来源仓库只被读取、绝不写入），在克隆内创建唯一验收分支并选择一个真实 `base..head`（≤20 个变更文件）；随后创建绑定版本、2 条需求（其中标题含 `P4-AI-IMPACT`），并在可用变更文件上尝试创建 1 个需求锚点（没有可锚定文件时记录 warning/skipped，不声称已建锚点），触发真实影响分析并校验：变更列表与带 50% rename 检测的 `git diff --name-status -z` 一致、AI 证据哈希按 commit 内容复算、候选带非空 reason/合法 confidence（high/medium 至少一条 path 证据）且命中需求、每个变更文件都被事实/锚点覆盖或进入 unmapped（§10.7 无静默遗漏）、unmapped 至少 1 条（默认强制；确需接受全映射 commit 对时显式传 `-AllowNoUnmapped`，输出与结果 JSON 会记录该放宽）、`stats.unmapped` 与列表一致（截断时除外）、二次 POST `reused=true` 且规范化候选 JSON 完全一致、同仓库/base 的第二个版本分析同一 head 时 `stats.cacheHits>0`、`force` 产生 `analysisVersion+1` 且旧分析不变；结果（含 `analysis.model`/`promptVersion` 与 degraded 标记）写入 `.tmp/p4-real-acceptance.json`。脚本默认 fail-closed：`llm_configured=false` 或任一分析（含复用 API 与 force 路径）返回 `degraded=true` 时直接失败；只有显式传 `-AllowDegraded` 才记录 `passed_degraded` 并跳过 AI-only 断言。可用 `EZTEST_ACCEPTANCE_P4_BASE`/`EZTEST_ACCEPTANCE_P4_HEAD` 指定真实 commit 对，用 `EZTEST_ACCEPTANCE_API_URL` 复用已运行的 API，或用 `-DryRun` 只检查 `.env` 键名、数据库目标 preflight 与工具链（不克隆、不启动 API、不调用 LLM）；脚本日志不含代码正文、提示词或密钥。

P4 的 Playwright E2E 已落地 5 个 spec（`versions-code.spec.ts`、`versions-code-cache.spec.ts`、`versions-code-degraded.spec.ts`、`versions-code-anchors.spec.ts`、`code-repos-index.spec.ts`），由 `scripts/e2e.ps1` 跑完整套件时执行（T20 记录 22/22 通过）；`scripts/acceptance.ps1` 也将这 5 个 spec 的静态存在性纳入 P4 检查。

## P5 用例脑图与设计评审

P5 新增 `mindmap` 限界上下文与 9 张 `case_*` 表（`case_node`/`case_step`/`case_assertion`/`case_source`/`case_review`/`case_review_finding`/`case_baseline`/`case_design_run`/`case_export`），把「用例脑图」占位页替换为完整闭环：从已确认需求生成场景/用例/步骤/断言/来源，人工编辑（节点 revision OCC）、代码锚点、AI/人工评审、blocker 确认、批准冻结基线、三格式导出，以及只读的代码影响提示。版本状态仍由 version 上下文唯一持有：`designing → reviewing → executing`，驳回回 `designing`，冻结后脑图只读 (readonly)。

- API 前缀 `/api/versions/{versionId}/mindmap`：图快照、节点/步骤/断言/来源/锚点 CRUD、`order`/`steps/order` 重排、`runs`（design/review/data_plan/explore）、`submit-review`/`review`/`reviews`/`findings`、`export`/`exports`/`exports/{id}/download`，共 23 个 path item / 27 个 operation；新增 8 个 `case.*` 事件并复用 `version:{id}` 通道与 `gate.pending/resolved`。
- 所有 mindmap ID（node/step/assertion/source/review/finding/run/export/baseline）在 JSON/URL/SSE 中都是十进制字符串；前端不得 `Number()`。P2 资源 ID（system/environment/code repo）保持 number。
- 人工编辑使用节点 revision 乐观并发；步骤/断言写操作带 `nodeRevision`。树规则（case 为叶子、scenario 可嵌套 2 层、每 case 至少 1 步骤/断言/已确认需求来源、seq 连续）由服务端事务内校验，失败 409 `invalid_case_tree`。
- 评审：`POST /mindmap/runs{kind=review}` 生成 findings；存在 open `blocker` 时批准必须 `acknowledgeFindings=true`（否则 409 `blocker_findings_open`）；批准写 `case_baseline`、节点 `draft → reviewed`、版本 `executing`，再次批准 409 `baseline_frozen`。
- 导出：服务端生成 `markdown`/`mermaid`/`opml`，文件写入 `EZTEST_EXPORT_DIR`（默认 `.data/exports`），DB 只存相对路径 + sha256 + 大小；同 `(version, format, baseline)` 复用同一导出。PNG 由前端把画布 SVG 转 canvas 后下载，不入库。
- 应用探索（D36）：仅管理员可触发 `kind=explore`；只发 GET/HEAD、按环境 `allowed_hosts` 白名单、拒绝私网/跨域重定向、最多 40 请求/深度 2/5s 超时/60s 总时长；产物写入 P2 应用模型并标 `source=explored`。普通用户 403 `forbidden`。
- 迁移：`order.txt` 末尾追加 13 条 P5 迁移——10 条既有上下文的 `*_rename_tables.sql`（旧表名→D34 规范名）、`resource_0005_app_model_explored_source.sql`、`codeintel_0004_case_anchor_subject.sql`、`mindmap_0001_cases.sql`。改名只追加、不保留兼容视图；`mindmap_0001_cases.sql` 只创建 `case_*` 新表。
- 新增 4 个环境变量：`EZTEST_EXPORT_DIR`（默认 `.data/exports`）、`EZTEST_MINDMAP_MAX_CASES`（60）、`EZTEST_EXPLORE_MAX_REQUESTS`（40）、`EZTEST_EXPLORE_TIMEOUT_MS`（5000），详见环境变量表。

P5 验收命令：

```powershell
# P5 单元 + 集成（仓库根 → backend）
cd backend
..\.tools\go\bin\go.exe test -count=1 ./internal/mindmap/...
$env:EZTEST_TEST_DATABASE_URL = "<eztest_test URL>"
..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/mindmap/...

# P5 前端组件（backend → frontend）
cd ..\frontend
pnpm vitest run src/features/mindmap

# 仅 P5 E2E（fake LLM P5 模式；仓库根）
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 mindmap

# 仅静态验收（P5 交付物/OpenAPI/迁移改名 + P4 fake-LLM；不需要数据库/API/Playwright）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly

# 完整 P0–P8 验收（P5 API 子检查在路由未接线时记录 SKIPPED，静态检查仍须 PASS）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1

# 真实 LLM 验收（gated；16 个 check，缺 llm_* 键 = 失败而非跳过）
$env:EZTEST_RUN_REAL_LLM = "1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-mindmap-llm.ps1

# 真实 LLM 验收 DryRun（只校验路径/env 键名/数据库目标与工具链，不调用 LLM）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-mindmap-llm.ps1 -DryRun
```

导出目录与探索白名单：

- 导出根目录由 `EZTEST_EXPORT_DIR` 决定（默认相对 API 进程工作目录的 `.data/exports`）；验收脚本会把它指向 `.tmp/` 下的临时目录，便于核对文件与 sha256。导出文件权限 0600、目录 0700，DB 不存正文。
- 探索只允许环境 `allowedHosts` 白名单内的 host；本机 fixture 需显式加入 `127.0.0.1`。探索入口仅 `role=admin` 可见/可用，普通用户调用 `kind=explore` 返回 403。
- 真实 LLM 脚本默认 fail-closed：`llm_configured=false`、design/review/data_plan run 非 `done`、缺真实锚点且无 `anchorsDropped` 原因，都会失败；只有显式删除默认 LLM 配置的降级 check 预期 `run=failed reason=ai_unavailable`。结果写入 `.tmp/p5-real-acceptance.json`（只含状态/ID/计数/错误码）。
- `scripts/acceptance.ps1` 的 P5 API 检查依赖 T16（`/mindmap` 路由）。在本分支 T16 完成前，脚本探测到未注册路由会把 8 个 API 子检查记为 `SKIPPED`，静态 3 项与 DoD 映射仍须 PASS。

P5 已知限制：不做浏览器探索（仅 HTTP GET/HEAD 的确定性 BFS + 可选 AI 归类）；PNG 由前端画布生成、无服务端渲染；导出格式为 Markdown/Mermaid/OPML（不生成 XMind）；冻结后不可解冻，需求重开后的增量重校验归 P7；`impactCases` 只是 P4 候选映射的只读提示。

## P6 用例执行（executor + 证据 + 暂停/恢复 + 报告）

P6 新增 `execution` 限界上下文：在 `executing` 版本上创建运行批次，由真实 Playwright executor（协议 A′）执行基线用例，记录步骤/断言/截图/HTTP 证据，支持优雅/立即暂停与检查点恢复、重启恢复、四态判定与失败分类（application/environment/session/model/case）、Batch 1 JSON/Markdown 报告，以及登录配方（`auth_flow`）驱动的自动登录、Session 失效重取与 MFA 人工接管。版本状态与暂停标记由 version 上下文镜像（`paused`/`pause_mode`/`batch_no`）。

- 迁移：`order.txt` 在 P5 迁移之后追加 `execution_0001_batches.sql`（`run_batch`/`case_run`/`step_result`/`assertion_result`/`evidence`/`evidence_ref` 6 表）、`version_0004_execution_state.sql`（version 的 3 个执行列）与 `resource_0006_auth_flows.sql`（`auth_flow` + `account_system_access.login_flow_id`）。
- API 前缀 `/api/versions/{id}`：`batches`（创建/列表/详情/暂停/恢复/取消）、`batches/{batchId}/case-runs[/{caseRunId}][/rerun]`、`batches/{batchId}/report[/export]`、`evidence/{evidenceId}[/meta]`、`execution/sessions`；登录配方 `/api/resources/auth-flows`（admin-only）。所有 execution ID 在 JSON/URL/事件/工具参数中都是十进制字符串。
- 执行事件（16 个）走既有 `version:{id}` 通道：`batch.created|started|paused|resumed|finished`、`run.started|paused|resumed|finished`、`step.started|finished`、`evidence.created`、`case.status_changed`、`human.requested`、`gate.pending`、`gate.resolved`。
- 证据只写不改：文件位于 `EZTEST_EVIDENCE_DIR`（默认 `.data/evidence`）下的 `{batch_id}/{case_run_id}/{evidence_id}.{ext}`，DB 只存相对路径 + sha256 + 大小；读取校验归属与哈希，响应带 `X-Content-SHA256`，不暴露文件系统路径。
- 失败分类：`failed(application)` 只来自有证据的断言失败；环境不可达/白名单 → `blocked(environment)`；Session 401/登录失败且重取一次仍失败 → `blocked(session)`；LLM/预算/工具内部错误 → `inconclusive(model)`；db 断言 → `inconclusive(case)`，绝不假通过。
- 端口（本机）：API `13333`、Playwright executor `13336`、fixture target `13335`、fake LLM `13334`、Web `6868`、PostgreSQL `5433`。executor 与 API 共享 `EZTEST_EXECUTOR_TOKEN`；executor 默认仅绑定回环，Compose 内网不发布宿主端口。

P6 验收命令（仓库根）：

```powershell
# 终端 1：Playwright executor（读取 .env 的 EZTEST_EXECUTOR_TOKEN）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-executor.ps1

# P6 Go 单元 + 集成（executor 协议/判定/调度/恢复/报告/HTTP 契约）
cd backend
..\.tools\go\bin\go.exe test -count=1 ./internal/execution/...
$env:EZTEST_TEST_DATABASE_URL = "<eztest_test URL>"
..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/execution/...

# executor 自测（node --test，不依赖数据库）
cd ..\executor
pnpm test

# P6 E2E（fake LLM + 真实 executor + fixture；仓库根）
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 execution

# 静态验收（交付物/迁移/OpenAPI/env/README，不启动数据库/API/executor）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly

# 完整 P0–P8 验收（P6 区块会自行启动 fixture/fake LLM/executor 与 API；
# 缺 executor/fixture/fake LLM 时 14 个 P6 API 子检查记录 SKIPPED，静态检查仍须 PASS；
# 其中 MFA 自动登录接管检查会经 /p6/login?mfa=1 走到 blocked(session)）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1

# 真实 LLM 验收（gated；gated 脚本会重建 eztest_p6_llm、真实模型执行 3 条用例 +
# 失败分类/暂停/重启/报告；缺 llm_* 键、executor 或健康检查失败即失败而非跳过）
$env:EZTEST_RUN_REAL_LLM = "1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-execution-llm.ps1

# DryRun（只校验路径/env 键名/DB URL/工具链，不调用 LLM）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-execution-llm.ps1 -DryRun
```

P6 证据与结果位置：

- MFA 自动登录接管验收：fixture 的 `/p6/login?mfa=1` 页面把查询串保留到表单 `action`，登录配方因此停在 `#mfa` 且不下发会话 Cookie；接受检查断言 `blocked(session, takeover_required)` + `needs_manual` 会话 + waiting takeover + `human.requested`/`gate.pending`。随后用普通 fixture 登录导出的 `storageState` 调用 `POST /api/resources/takeovers/{id}/complete`，断言 `gate.resolved(takeover_completed)`、新 attempt 自动入队并最终 `passed`；若后端未持久化 `case_run.session_id`，该子路径会以明确的 SKIP/限制记录而不是假通过。
- `scripts/acceptance.ps1` 的 P6 证据根：`.tmp/p6-evidence/`（每批次/用例/证据 ID 分目录）；API/executor/fake LLM/fixture 日志在 `.tmp/acceptance-p6-*.log`。
- 真实 LLM 验收证据根：`.tmp/p6-real-evidence/`；结果 JSON：`.tmp/p6-real-acceptance.json`（只含状态/ID/计数/耗时，不含 prompt、密钥或证据正文）。
- P6 环境变量：`EZTEST_EXECUTOR_URL`、`EZTEST_EXECUTOR_TOKEN`、`EZTEST_EVIDENCE_DIR`、`EZTEST_EXEC_MAX_CONCURRENT_CASES`（1–8）、`EZTEST_EXEC_MAX_TOOL_CALLS`（1–1000）、`EZTEST_EXEC_CASE_TIMEOUT_MS`（1000–3600000）、`EZTEST_EXEC_MAX_CASES_PER_BATCH`（1–1000）、`EZTEST_EXEC_WORKER_INTERVAL_MS`（100–60000）、`EZTEST_EXEC_EVIDENCE_MAX_BYTES`（1 KiB–100 MiB）、`EZTEST_EXEC_EVIDENCE_PER_CASE`（1–1000），默认值与说明见 `.env.example` P6 段。
- 运行环境要求：Node 20+、Playwright 1.64.1 的 Chromium（或系统 Chrome/Edge，executor 会回退 channel `chrome`/`msedge`）、本机 PostgreSQL；真实 LLM 验收额外需要可用的 `llm_base_url`/`llm_key`/`llm_model_name` 与网络。

P6 当时限制（P7/P8 已更新）：单进程调度在 P8 由 PostgreSQL advisory lock 单活；P6 阶段不支持 db 断言（P7 已补只读 db 断言）；不做用例级自动重跑（应用失败仅手动 rerun）；证据 GC 已于 P8 交付（只清执行证据文件）；真实 LLM 的分类/暂停结果受模型行为影响，脚本在不符合预期时 fail-closed。

## P7 全自主闭环与交付（补全 + 失败分析 + 问题/报告 + 继续中）

P7 新增 `delivery` 限界上下文（问题/报告/分析 run），并加法扩展 version/resource/execution/mindmap/codeintel：基线批次终态后自主编排出报告与补全，完成后新增需求进入 `continuing`，经增量设计/增量批次/报告 revision 回到 `completed`；失败分析/问题整理不可用时批次仍照常执行并标记降级。

- **继续中（D43）**：在 `completed` 版本上成功创建需求会在同事务置 `status=continuing`、`continue_phase=clarifying`、`continued_at`（D31 唯一例外；确认/重开需求不改状态）；`batch_no` 只在建批时分配。`continuing → completed` 由自主协调器在增量批次链收尾后 `TryFinalize` 触发，取代 P6 的“批次完成即版本完成”。
- **链式批次（D45）**：`run_batch.kind ∈ {baseline,incremental,supplement}` + `parent_batch_id`；单版本单活跃批；每批最多 1 个补全周期、每周期最多 5 条新用例；补全批由 worker 创建，用户不能手工建 supplement 批（409 `supplement_batch_automatic`）。增量批 base=上一终态批 `head_commit`，head=建批时固定分支 HEAD；非祖先（rebase/强推）回退版本基线并标 `base_fallback_reason=history_rewritten`。
- **自主链（D51）**：单进程 worker 默认 2s tick，单 tick 最多处理 `EZTEST_AUTONOMY_BATCHES_PER_TICK`（默认 4）个终态批/继续中版本；顺序为失败分析（仅 `failed(application)`）→ 问题整理 → 报告 ensure/stale 重生成 → 基线批补全 → supplement 批；`autonomy_done`/`supplement_attempted` CAS 防重复建批。3 个 profile：`supplement-agent`、`failure-analysis-agent`、`issue-triage-agent`，MaxTurns 20 / 600s。
- **问题（D47）**：`issue` + `issue_ref`；状态 `open → confirmed/dismissed`（终态，不重开）；AI 只为 `failed(application)` 与需求缺口建单，环境/会话/模型/用例失败不生成缺陷；同一 `sha256(version|caseNode|failureClass|normalizedReason)` 去重；确认/驳回任一登录用户可用，驳回必填原因、revision CAS；`code_anchor(subject_kind='issue')` 由服务端校验 `file:line` 与问题归属后创建。
- **报告（D46）**：`report` 按 `(batch_id, revision)` 唯一；分析终态后自动发布 revision 1；读取时用 `input_fingerprint` 判 stale 并只发一次 `report.stale`；重新生成 revision+1（有新 revision 时返回最新 200，否则新建 201）；支持批次差异与 Markdown（含 Mermaid）导出。P6 `GET /batches/{id}/report` 派生端点保持原样。
- **DB 断言（D48）**：环境级 `db_target`（admin-only，PostgreSQL）；密码明文存储（D28 同类例外）但 API/DTO/事件/审计/日志/证据/报告/prompt 永不回显；只读事务 + 单条 `SELECT/WITH/SHOW/EXPLAIN` + 超时/行数/SQL 与结果字节上限；连接/权限失败 → `blocked(environment)`，SQL/只读/截断错误 → `inconclusive(case)`；结果落 `db_result` 证据。
- **API/事件/权限（D52）**：版本级 `/api/versions/{id}/issues`、`/reports`、`/batches/{batchId}/analysis`；全局只读 `/api/issues`、`/api/reports`；环境级 `/api/resources/environments/{id}/db-target`（admin）；mindmap `incremental-plan` 与 `PATCH .../nodes/{nodeId}/supplement`；`baseCommit` 可选与 issue 锚点；`/api/versions/{id}/changes` 读时派生（`version_change` 不建表，`code_changed` 留 P8）。新增 8 个事件（`case.supplemented`、`case.revalidation_marked`、`issue.created/updated`、`report.published/stale`、`analysis.started/finished`）走既有 `version:{id}` 通道；P7 ID 一律十进制字符串。
- **前端**：问题/报告 Tab、总览完善、继续中徽章/横幅 + 增量计划、admin-only DB 目标区块；全局只读中心 `/issues`、`/reports`；深链 `?tab=issues&issue=`、`?tab=report&report=`、`?tab=results&caseRun=`、`?tab=requirements&requirement=`。
- **迁移**：`order.txt` 追加 `delivery_0001_issue.sql`、`delivery_0002_report.sql`、`delivery_0003_analysis.sql`、`resource_0007_db_target.sql`、`version_0005_continue.sql`、`execution_0002_autonomy.sql`、`mindmap_0002_supplement.sql`、`codeintel_0005_issue_anchor.sql`。

P7 验收命令（仓库根）：

```powershell
# P7 后端单元（集成测试需 EZTEST_TEST_DATABASE_URL + -tags=integration）
cd backend
..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/... ./internal/version/... ./internal/execution/... ./internal/resource/... ./internal/mindmap/... ./internal/codeintel/... ./cmd/eztest/...

# P7 前端单元（问题/报告/继续中/增量计划/DB 目标/总览）
cd ..\frontend
pnpm vitest run src/features/delivery src/features/versions src/features/resources src/features/execution

# P7 fake-LLM 契约（补全/失败分析/问题整理/DB 断言确定性）
cd ..
node scripts/fake-llm.p7.test.cjs

# 静态验收（P7 交付物/迁移/OpenAPI/事件/reason/testid；不需要数据库/API）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly

# P7 Playwright E2E（已通过记录（见 acceptance.md）；执行时由 e2e.ps1 管理服务、fake LLM 与 fixture）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p7-continuing
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p7-issues-report
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p7-supplement
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p7-degraded
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 p7-db-config

# 完整 P0–P8 验收（P6 区块自行启动 fixture/fake LLM/executor/API；
# P7 区块仅为仓库本地静态检查，不启动 P7 fixture/API；缺 P5/P6 依赖时对应 API 子检查记录 SKIPPED，静态仍须 PASS）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1
```

P7 真实验收（gated，显式开启才产生费用/连接真实库）：

```powershell
# 真实 LLM：补全/失败分析/问题整理/报告（gated；已通过记录（见 acceptance.md））
$env:EZTEST_RUN_REAL_LLM = "1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p7-llm.ps1

# 真实 PostgreSQL 只读 DB 断言（gated；已通过记录（见 acceptance.md）；先用 fixture-db.ps1 准备目标库/只读角色）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/fixture-db.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-p7-db.ps1
```

P7 证据与结果位置：

- fake LLM / E2E：`.tmp/` 下由 `scripts/e2e.ps1` 管理；DB fixture 目标库 `eztest_p7_target`（只读角色 `eztest_p7_ro`），只用于本机验收。
- 报告 Markdown：`EZTEST_REPORT_DIR`（默认 `.data/reports`），DB 只存相对路径 + sha256 + 大小；导出响应带 `X-Content-SHA256`。
- gated 真实验收结果写入 `.tmp/`（只含状态/ID/计数/错误码，不含 prompt、凭据、SQL 结果或证据正文）。

P7 当时限制（P8 已收口/更新）：`version_change` 表与 `code_changed` 变更项已在 P8 落地（`GET /api/versions/{id}/code/changes` 的 `batchTimeline`）；证据 GC、报告 stale 联动、多实例 leader 锁已在 P8 落地；报告快照已写入 `baseFallbackReason`/`historyRewritten`；`EZTEST_ANALYSIS_MAX_CASES`/`EZTEST_SUPPLEMENT_MAX_CASES` 与 `EZTEST_AUTONOMY_BATCHES_PER_TICK` 的接线仍以 P8 发布说明为准（见下文“P8 已知限制”）。报告 PDF、问题外部工单、问题重开/合并、非 PostgreSQL 目标库仍不在 P8 范围（P9/后续）。

## P8 体验完善与硬化

P8 在 P0–P7 之上完成“可长期使用/可运维”的体验与硬化，**不新增业务域**。截至本提交（Task 33 文档同步）的实际交付状态：

| 能力 | 状态 | 入口/证据 |
|---|---|---|
| 探索链路 v2（服务端读模型 + React Flow 画布 + 图例/Sheet/MiniMap/跟随/截断/ARIA） | 已交付 | `GET /api/versions/{id}/exploration-chain`；`frontend/e2e/p8-exploration-chain.spec.ts`；链路专项 SLO PASS |
| 顶栏版本切换器、全局搜索、待办中心、移动端导航 | 已交付 | `/api/search`、`/api/todos`；`Ctrl/Cmd+K`；30s 轮询 |
| 脑图回版本 Tab（保留全屏路由与 `?case=`） | 已交付 | `?tab=mindmap` 与 `/versions/{id}/mindmap` |
| 代码与变更完整版（`version_change` 快照 + 批次时间线 + 事实人工修正 + 质量区块） | 已交付 | `GET /api/versions/{id}/code/changes`、`GET /api/code/repos/{repoId}/quality`、`POST .../facts/{factId}/reviews` |
| 报告 revision 选择器/D49 快照/stale 联动 | 已交付 | `?report=`；升级后旧报告首次读取 stale 一次（见发布说明） |
| Agent 中心（13 角色目录、运行观察、质量指标、只读） | 已交付 | `/agents`、`/api/agent-center/*` |
| 版本内知识沉淀（草稿→审核→批准→注入） | 已交付 | `/knowledge`、`/api/versions/{id}/knowledge`；概览内提交面板 |
| 评测平台/质量中心（确定性门禁 + 真实 LLM 报告） | 已交付 | `/quality`、`/api/quality/eval-runs`、`scripts/eval.ps1` |
| 限流/SLO、证据 GC、备份恢复、多实例单活、安全审计/头 | 已交付（部分验收项待最终运行） | `scripts/slo.ps1`/`slo-chain.ps1`/`backup.ps1`/`security-audit.ps1`；`docs/acceptance/p8/` |
| **视觉断言（截图基线/像素比对/基线审批）** | **部分提交、运行未接线（D62）** | 迁移/编辑/审批/executor `visual.compare`/查看器已提交；但 `main/config` 装配与 agent 执行入口未接线，当前 `/visual` 路由不挂载、真实运行不会产生 visual 证据；OpenAPI 暂不发布该契约 |
| 全量 SLO 真实测量、恢复库应用 smoke、最终真实验收 | **待最终验收（Task 35）** | `docs/acceptance/p8/slo.md` 目前是 SKIP 报告；备份演练未做应用 smoke |

P8 新增/变更模块：

- 后端：`internal/chain`（链路只读读模型）、`internal/search`（跨上下文搜索聚合）、`internal/workspace`（待办）、`internal/quality`（评测）、`platform/leader`（advisory lock）、evidence GC、knowledge、`version_change`/事实修正/质量聚合。
- 前端：`features/execution/chain`（React Flow）、`features/shell/{search,todos}`、`features/knowledge`、`features/quality`、Agent 中心只读化、移动导航、证据对比、报告 revision 选择器。
- 部署：`deploy/` 增加证据/报告/导出卷与代码仓只读挂载示例；**本机没有 Docker，Compose 只做静态校验、未实跑**。
- 迁移（append-only）：`codeintel_0006_version_change.sql`、`codeintel_0007_fact_review.sql`、`version_0006_knowledge.sql`、`quality_0001_eval.sql`、`execution_0004_evidence_purge.sql` 等；视觉迁移在合并时追加。

P8 验收命令（仓库根）：

```powershell
# P8 后端定向单元（集成测试需 EZTEST_TEST_DATABASE_URL + -tags=integration）
cd backend
..\.tools\go\bin\go.exe test -count=1 ./internal/chain/... ./internal/search/... ./internal/workspace/... ./internal/quality/... ./internal/execution/... ./internal/version/... ./internal/codeintel/... ./internal/platform/leader/...
..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/chain/... ./internal/quality/... ./internal/execution/... ./internal/codeintel/... ./internal/platform/leader/...

# P8 前端定向单元
cd ..\frontend
pnpm vitest run src/features/execution src/features/shell src/features/knowledge src/features/quality src/features/agent-console src/features/versions src/features/code-intel

# E2E（e2e.ps1 自管服务/DB/fake LLM/fixture/executor）
cd ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 exploration-chain
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 mobile-nav

# 评测静态自检 / SLO 静态预检 / 安全审计（离线）/ 备份演练
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -DryRun
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1 -SkipLive
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test-backup.ps1

# 全量静态验收
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly
```

P8 文档入口：部署 [`docs/deployment.md`](docs/deployment.md)、运维 [`docs/operations.md`](docs/operations.md)、用户 [`docs/user-guide.md`](docs/user-guide.md)、开发 [`docs/development.md`](docs/development.md)、发布说明 [`docs/release-notes/p8.md`](docs/release-notes/p8.md)。



## 技术架构

### 后端（`backend/`）

- Go 1.24，标准库 `net/http`（Go 1.22+ 方法路由），`pgx/v5` 连接 PostgreSQL 17。
- DDD 限界上下文：`identity`、`eventing`、`llm`、`storage`、`audit`、P1 的 `agent`、P2 的 `resource`、P3 的 `version`、P4 的 `codeintel`、P5 的 `mindmap`、P6 的 `execution`、P7 的 `delivery` 与 P8 的 `chain`/`search`/`workspace`/`quality`；每个上下文四层（`domain` / `application` / `infrastructure` / `interfaces/httpapi`），导入方向约束见 `Agent.md` §6.2。
- `cmd/eztest` 是唯一组装根：读取配置、连接数据库、执行迁移、种子管理员/默认 LLM 配置/内置 Agent profile、清理 `interrupted` run、装配 HTTP 服务/workers/leader 锁，在 SIGINT/SIGTERM 时先停止 Agent run 再优雅退出。
- `internal/server` 只装配路由与中间件：recover → request ID → 日志 → CORS → 认证 → 限流 → 幂等 → 路由。
- `internal/platform` 放共享的纯基础设施：错误信封、分页游标、JSON 解码、请求 ID、AES keyring、幂等存储、P8 `leader` advisory lock 原语。
- 迁移嵌入二进制，启动时在 advisory lock 下按版本顺序事务执行；重复启动无副作用。
- 启动时自动执行迁移，无需单独 migrate 命令；`schema_migrations` 记录版本与校验和。

### 前端（`frontend/`）

- Next.js 16 App Router、React 19、TypeScript、Tailwind CSS 4、Radix 原语、`next-themes`、lucide-react、`@xyflow/react` 12。
- `src/app/` 只做路由/布局/页面组装；业务在 `src/features/{auth,users,llm-settings,sse-test,agent-console,resources,code-repos,versions,code-intel,mindmap,execution,delivery,shell,knowledge,quality}`；共享 UI 原语在 `src/components/ui/`，跨 feature 导航在 `src/navigation/`。P8 新增 `shell/search`/`shell/todos`、`execution/chain`（React Flow）、`knowledge`、`quality`；`/agents`、`/knowledge`、`/quality` 为 P8 页面。
- 所有 HTTP 请求经 `src/lib/api-client.ts`（Bearer、错误信封、`X-Request-ID`、幂等键）；SSE 经 `src/lib/sse-client.ts`（fetch 流、`Last-Event-ID`、退避重连、心跳超时）。
- 质量工具：Biome（lint/format）、Vitest + Testing Library、Playwright（使用本机 Chrome/Edge channel，不下载 Chromium）。
- 安全头：`next.config.ts` 下发 `X-Content-Type-Options`/`Referrer-Policy`/`X-Frame-Options`/`Permissions-Policy` 与 report-only CSP；强制 CSP/TLS/HSTS 由部署侧负责。

### 部署（`deploy/`）

- `Dockerfile.api`：Go 多阶段构建，Alpine 运行时，非 root 用户，镜像内带 `docs/openapi/api.yaml`，创建 `objects`/`attachments`/`evidence`/`reports`/`exports` 目录。
- `Dockerfile.web`：Next.js `standalone` 多阶段构建，非 root 用户，监听 6868。
- `docker-compose.yml`：`api`（13333）、`web`（6868）、`postgres:17`（5433）、内部 `executor`（13336）；健康检查、`pgdata`/`objects`/`attachments`/`evidence`/`reports`/`exports` 卷、启动顺序；注释提供代码仓只读挂载示例。
- 本机没有 Docker，Compose 只做静态校验，**未运行过容器**；详见 [`deploy/README.md`](deploy/README.md) 与 [`docs/deployment.md`](docs/deployment.md)。

## 目录结构

```text
EzTest/
├─ backend/
│  ├─ go.mod
│  ├─ cmd/eztest/main.go              # 唯一组装根
│  └─ internal/
│     ├─ config/                      # env 解析/校验
│     ├─ db/                          # pgx pool、事务、嵌入式迁移
│     │  └─ migrations/
│     ├─ server/                      # 路由装配、中间件链
│     ├─ platform/                    # 错误/分页/请求 ID/crypto/幂等
│     ├─ identity/                    # 用户/认证（四层）
│     ├─ eventing/                    # 事件/SSE（四层）
│     ├─ llm/                         # LLM 配置/网关（四层）
│     ├─ agent/                       # P1 Agent 运行时（四层）
│     ├─ resource/                    # P2 公共资源/身份（四层）
│     ├─ version/                     # P3 版本/需求/澄清（四层）
│     ├─ codeintel/                   # P4 代码理解/影响分析（四层）
│     ├─ mindmap/                     # P5 用例脑图/评审（四层）
│     ├─ execution/                   # P6 执行/证据/暂停恢复 + P8 GC/leader 接线
│     ├─ delivery/                    # P7 问题/报告/分析（四层）
│     ├─ chain/                       # P8 探索链路只读读模型（四层）
│     ├─ search/                      # P8 全局搜索聚合（四层）
│     ├─ workspace/                   # P8 待办聚合（四层）
│     ├─ quality/                     # P8 评测结果（四层）
│     ├─ storage/                     # 对象存储（四层）
│     └─ audit/                       # 审计（四层）
├─ executor/                          # P6 独立 Node/Playwright 执行器
├─ frontend/
│  ├─ src/app/                        # Next.js App Router 页面组装（含 /agents、/resources、/code-repos、/versions、/issues、/reports、/knowledge、/quality）
│  ├─ src/features/                   # auth/users/.../shell/knowledge/quality（P8 新增 shell/search、todos、execution/chain、knowledge、quality）
│  ├─ src/components/ui/              # 共享 UI 原语（StatusBadge/EmptyState）
│  ├─ src/navigation/                 # 跨 feature 导航数据（侧边栏）
│  ├─ src/lib/                        # api-client / sse-client / auth-storage
│  ├─ scripts/validate-compose.mjs    # Compose 静态校验
│  └─ e2e/                            # Playwright E2E（由 scripts/e2e.ps1 编排；P8 含 p8-exploration-chain/mobile-nav）
├─ deploy/                            # Dockerfile.api / Dockerfile.web / docker-compose.yml
├─ docs/                              # openapi、design、acceptance、deployment/operations/user-guide/development、release-notes
├─ eval/                              # P8 评测语料/真值/基线
├─ scripts/                           # db/dev-api/dev-web/test/e2e/acceptance/eval/slo/backup/restore/security-audit/fake-llm/acceptance-*
├─ docs/openapi/api.yaml              # 手工维护的 OpenAPI 3.0（P0–P8）
├─ .env.example                       # 环境变量模板（无真实秘密）
├─ .tools/                            # 便携 Go/PostgreSQL（gitignored）
└─ README.md
```

## 本地工具链（Windows）

要求：Windows 10/11、PowerShell 5.1+（`powershell.exe`；本机没有 `pwsh` 时所有脚本仍可运行）、Node.js 22+、pnpm 10/11。

仓库自带便携工具链脚本（`gitignored` 的 `.tools/`）：

```powershell
# 1) 复制环境变量模板并填写占位符（生成 EZTEST_SECRET_KEYS 的片段见下节）
Copy-Item .env.example .env

# 2) 首次准备便携 Go 1.24 + PostgreSQL 17，初始化/启动 PG 并创建 eztest、eztest_test
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .tools/setup-toolchain.ps1

# 或：工具链已存在时用仓库脚本管理 PG
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/db.ps1 init
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/db.ps1 status
```

已有本机工具链时，日常只需：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/db.ps1 start
```

数据库端口固定为 `5433`；`eztest` 为开发库，`eztest_test` 为集成测试库。

## 环境变量

完整模板见 [`.env.example`](.env.example)。`.env`、`.tools/`、`docs/*.local.md` 均被 gitignore，**禁止提交真实密钥**。P8 知识注入变量（`EZTEST_KNOWLEDGE_*`）与未接线的链路上限/视觉变量见下表，需要时手动补进 `.env`；`.env.example` 当前只含限流/GC/leader 段。

| 变量 | 必填 | 说明 |
|---|---|---|
| `EZTEST_ENV` | 否 | `dev`（默认）/ `prod`；`prod` 默认关闭测试端点。 |
| `EZTEST_HTTP_ADDR` | 否 | API 监听地址，默认 `:13333`。 |
| `EZTEST_WEB_ORIGIN` | 否 | CORS 允许的前端 origin，默认 `http://localhost:6868`。 |
| `EZTEST_DATABASE_URL` | 是 | PostgreSQL URL；本地为 `postgres://eztest:<password>@localhost:5433/eztest?sslmode=disable`。 |
| `EZTEST_TEST_DATABASE_URL` | 集成测试 | 独立测试库 URL（`eztest_test`）；库名必须以 `_test`/`_e2e` 结尾且不得与开发库相同（测试辅助与脚本会前置校验，失败即拒绝重置 schema）。 |
| `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD` | 首次启动 | 种子管理员；密码至少 10 字符，仅存 bcrypt 哈希。 |
| `EZTEST_SECRET_KEYS` | 是 | JSON `{"版本":"base64 32 字节密钥"}`，用于加密 LLM Key/headers。 |
| `EZTEST_SECRET_KEY_ACTIVE` | 是 | 当前加密密钥版本，必须在 `EZTEST_SECRET_KEYS` 中存在。 |
| `EZTEST_STORAGE_DIR` | 否 | 对象根目录，默认 `.data/objects`（容器内 `/data/objects`）。 |
| `EZTEST_ENABLE_TEST_ENDPOINTS` | 否 | 是否注册 `POST /api/admin/stream/test-events`，dev 默认 true。 |
| `EZTEST_LOG_LEVEL` | 否 | `debug` / `info` / `warn` / `error`。 |
| `EZTEST_OPENAPI_PATH` | 否 | OpenAPI 文件路径，默认 `docs/openapi/api.yaml`；相对路径以进程工作目录为准，`dev-api.ps1` 会设为绝对路径。 |
| `EZTEST_VERSION_ATTACHMENT_DIR` | 否 | P3 附件文件系统根目录，默认 `.data/version-attachments`（容器内 `/data/version-attachments`）；启动时创建（目录 0700、文件 0600）。 |
| `EZTEST_SNOWFLAKE_NODE_ID` | 否 | P3 雪花 ID 节点 ID，0–1023，默认 `1`；多实例部署必须各不相同。 |
| `EZTEST_AGENT_FILE_ROOT` | 否 | `file.list`/`file.read` 的只读根；**空 = 不注册文件工具**（安全默认）。本地用 `scripts/dev-api.ps1` 时未设置则默认仓库根。 |
| `EZTEST_AGENT_HTTP_ALLOWLIST` | 否 | `http.get` 允许的 host 列表（逗号分隔，可含端口），默认 `127.0.0.1,localhost`；空列表启动失败。 |
| `EZTEST_AGENT_TOOL_TIMEOUT_MS` | 否 | 单工具超时，1000–60000 ms，默认 `10000`。 |
| `EZTEST_AGENT_MAX_CONCURRENT_RUNS` | 否 | 全局并发 run 上限，1–32，默认 `4`。 |
| `EZTEST_CODEINTEL_MAX_CHANGED_FILES` | 否 | P4 单次分析最多送入 LLM 的变更文件数，1–200，默认 `20`；超出截断并计入 stats。 |
| `EZTEST_CODEINTEL_MAX_FILE_BYTES` | 否 | P4 单文件参与分析/读取的最大字节，1024–1048576，默认 `65536`。 |
| `EZTEST_CODEINTEL_MAX_INDEX_FILES` | 否 | P4 单次 T0 索引落库文件上限，1–50000，默认 `5000`；超出 `truncated=true`。 |
| `EZTEST_CODEINTEL_ANALYSIS_TIMEOUT_MS` | 否 | P4 单次分析 run 总超时，30000–600000 ms，默认 `180000`。 |
| `EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES` | 否 | P4 worker 并发上限（单实例），1–4，默认 `1`。 |
| `EZTEST_CODEINTEL_PROMPT_VERSION` | 否 | P4 提示词版本，1–32 字符 `[a-z0-9.-]`，默认 `p4.1`；参与缓存键与 fact 版本。 |
| `EZTEST_CODEINTEL_MAX_EXCERPT_LINES` | 否 | P4 证据摘录最大行数，1–200，默认 `40`（硬上限 200）。 |
| `EZTEST_CODEINTEL_MAX_DIFF_BYTES` | 否 | P4 单文件 unified diff 返回上限，4096–1048576，默认 `262144`；超出 `truncated=true`。 |
| `EZTEST_EXPORT_DIR` | 否 | P5 导出文件系统根目录，默认 `.data/exports`；DB 只存相对路径 + sha256 + 大小，文件 0600、目录 0700。 |
| `EZTEST_MINDMAP_MAX_CASES` | 否 | P5 单次设计 run/物化最多 case 数，1–60，默认 `60`。 |
| `EZTEST_EXPLORE_MAX_REQUESTS` | 否 | P5 单次探索最多 HTTP 请求数，1–40，默认 `40`；超出记 `stats.truncated`。 |
| `EZTEST_EXPLORE_TIMEOUT_MS` | 否 | P5 探索单请求超时，500–30000 ms，默认 `5000`；另有 60s 总时长与 512 KiB 解析上限。 |
| `EZTEST_EVIDENCE_DIR` | 否 | P6 执行证据根目录，默认 `.data/evidence`；DB 只存相对路径 + sha256 + 大小；P8 GC 只清理此目录下的证据文件。 |
| `EZTEST_REPORT_DIR` | 否 | P7 报告 Markdown 根目录，默认 `.data/reports`；目录 0700、文件 0600，DB 只存相对路径 + sha256 + 大小。 |
| `EZTEST_AUTONOMY_BATCHES_PER_TICK` | 否 | P7 自主 worker 单 tick 处理的终态批/继续中版本上限，默认 `4`；未设置或非法值回退默认（规格建议 1–20，当前实现未钳制上限）。 |
| `EZTEST_ANALYSIS_MAX_CASES` | 否 | P7/P8 规格目标：失败分析单 run 最多用例数（默认 `20`，1–100）；**当前构建仍以代码固定上限 20 执行，尚未读取该变量**（见 P8 已知限制）。 |
| `EZTEST_SUPPLEMENT_MAX_CASES` | 否 | P7/P8 规格目标：补全单 run 最多新用例数（默认 `5`，1–20）；**当前构建仍以代码固定上限 5 执行，尚未读取该变量**（见 P8 已知限制）。 |
| `EZTEST_DB_RESULT_MAX_BYTES` | 否 | P7 db 断言单次结果字节上限，64 KiB–16 MiB，默认 `1048576`。 |
| `EZTEST_DB_SQL_MAX_BYTES` | 否 | P7 db 断言 SQL 文本上限，1 KiB–64 KiB，默认 `16384`。 |
| `EZTEST_RATE_LIMIT_RPS` | 否 | P8 每用户（认证后）/IP（公开路由）令牌桶速率，0–1000，默认 `50`；`0` 关闭普通令牌桶（SSE 并发上限仍生效）。超限返回 429 `rate_limited` + `Retry-After`。 |
| `EZTEST_RATE_LIMIT_BURST` | 否 | P8 令牌桶突发容量，1–5000，默认 `100`。 |
| `EZTEST_RATE_LIMIT_SSE_MAX` | 否 | P8 每用户/IP 并发 SSE 流上限，1–100，默认 `10`；超并发 429。SSE 心跳不计入普通桶。 |
| `EZTEST_GC_ENABLED` | 否 | P8 是否允许**非 dry-run** 证据 GC，默认 `false`（`POST /api/admin/gc` 默认/仅 dry-run；`false` 时显式执行返回 409）。 |
| `EZTEST_GC_RETENTION_DAYS` | 否 | P8 未引用证据保留天数，1–3650，默认 `30`。 |
| `EZTEST_GC_BATCH_KEEP` | 否 | P8 最近 N 批证据始终受保护，1–50，默认 `3`。 |
| `EZTEST_INSTANCE_ID` | 否 | P8 多实例 leader 锁的实例标识，出现在启动/退出与锁日志；默认 `hostname-pid`，最长 64 字符。 |
| `EZTEST_LEADER_LOCK_KEY` | 否 | P8 PostgreSQL advisory lock 基础 key（int64），默认 `1163411512`；调度、自主 worker、每日维护派生出相邻 key，所有 API 实例必须一致。 |
| `EZTEST_KNOWLEDGE_MAX_INJECT` | 否 | P8 版本知识注入条数上限，0–20，默认 `5`；`0` 关闭条数预算。 |
| `EZTEST_KNOWLEDGE_MAX_BYTES` | 否 | P8 版本知识注入字节上限，0–16384，默认 `4096`；超出截断并记 `knowledge_truncated`。 |
| `EZTEST_CHAIN_MAX_NODES` | 否 | P8 规格目标（默认 300，50–2000）；**当前构建使用代码默认/请求 `limit`，未读取该环境变量**。 |
| `EZTEST_SEARCH_MAX_RESULTS_PER_TYPE` | 否 | P8 规格目标（默认 5，1–20）；**当前由请求 `limit` 控制，未读取该环境变量**。 |
| `EZTEST_TODOS_MAX` | 否 | P8 规格目标（默认 50，1–200）；**当前由请求 `limit` 控制，未读取该环境变量**。 |
| `EZTEST_VISUAL_DIFF_THRESHOLD` / `EZTEST_VISUAL_BASELINE_MAX_BYTES` | 否 | P8 视觉断言规格目标；`EZTEST_VISUAL_BASELINE_MAX_BYTES` 仅 executor 读取（默认 5 MiB），后端 config/Compose 未接线；`EZTEST_VISUAL_DIFF_THRESHOLD` 未使用。**视觉断言运行入口未接线，当前不可用**（见发布说明）。 |
| `llm_base_url` / `llm_key` / `llm_model_name` | 否 | 首次启动的可选默认 LLM 种子；小写变量名是设计约定，Key 留空则不发起真实调用。P4 复用该配置，不新增模型/Key。 |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | Compose | 仅 `deploy/docker-compose.yml` 使用；`POSTGRES_PASSWORD` 必填。 |
| `NEXT_PUBLIC_API_BASE_URL` | 否 | 浏览器访问的 API origin，默认 `http://localhost:13333`；Compose 构建时内联。 |

生成 `EZTEST_SECRET_KEYS`（PowerShell，32 字节随机密钥）：

```powershell
$bytes = New-Object byte[] 32
[System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
'{"1":"' + [Convert]::ToBase64String($bytes) + '"}'
```

把输出整行写入 `.env`，并保持 `EZTEST_SECRET_KEY_ACTIVE=1`。轮换时新增版本（如 `"2"`）再提升 active，旧版本需保留以便解密历史数据。

## 本地启动

```powershell
# 终端 1：API（自动执行迁移、种子管理员；13333）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-api.ps1

# 终端 2：前端（6868；首次先 cd frontend; pnpm install）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-web.ps1
```

打开：

- Web：<http://localhost:6868>
- Agent 控制台：<http://localhost:6868/agents>（管理员登录后；普通用户显示仅管理员可用）
- 公共资源：<http://localhost:6868/resources>（四 Tab；普通用户只读）
- 代码仓：<http://localhost:6868/code-repos>（普通用户只读）
- 版本工作台：<http://localhost:6868/versions>（列表与 `/versions/[id]` 详情；全部登录用户可写；P8 十个 Tab 含脑图/链路/问题/报告/代码）
- 全局问题中心：<http://localhost:6868/issues>（P7 只读，可深链回版本问题 Tab）
- 全局报告中心：<http://localhost:6868/reports>（P7 只读，可深链回版本报告 Tab）
- 知识库：<http://localhost:6868/knowledge>（P8；普通用户已批准只读，管理员审核队列）
- 质量中心：<http://localhost:6868/quality>（P8；仅管理员）
- 设置与审计：<http://localhost:6868/settings>（管理员；含审计日志/基础配置）
- 用例脑图：<http://localhost:6868/versions/[id]/mindmap>（全屏画布；冻结后只读）
- API 健康：<http://localhost:13333/api/health>
- OpenAPI：<http://localhost:13333/api/openapi.yaml>

等价的底层命令：

```powershell
# API（必须从 backend/ 运行；OpenAPI 默认相对路径按进程工作目录解析，
# 因此显式指向仓库根目录的文件）
cd backend
$env:EZTEST_OPENAPI_PATH = (Resolve-Path ..\docs\openapi\api.yaml).Path
# 可选：启用只读文件工具（不设置时 file.list/file.read 不注册）
$env:EZTEST_AGENT_FILE_ROOT = (Resolve-Path ..).Path
..\.tools\go\bin\go.exe run ./cmd/eztest

# 前端
cd frontend
pnpm dev
```

首次登录使用 `EZTEST_ADMIN_USERNAME` / `EZTEST_ADMIN_PASSWORD`（默认用户 `admin`）。本地启用 Agent 文件工具的方式见上节「P1 Agent 运行时」。

### 开发管理员密码

本地管理员密码只存在于两处，二者都被 gitignore：

- 仓库根 `.env` 的 `EZTEST_ADMIN_PASSWORD`（Compose/脚本读取）；
- 本机生成的 `docs/dev-setup.local.md`（`.tools/gen-env.ps1` 首次生成时写入，仅本机可见）。

数据库中只存 bcrypt 哈希；API 响应、日志、审计与错误信封都不会回显密码。不要把这两个文件提交或粘贴到 issue/PR。

## API 概览

基础约定（详见 OpenAPI 与 P0 规格 §9、P1 规格 §9–§10、P2 规格 §7）：

- 成功：`{"data": ...}`；列表：`{"data":[...],"next_cursor":"...","has_more":true}`。Agent session 列表是例外：`{"data":{"items":[...],"has_more":true,"next_cursor":null}}`。
- 错误：`{"error":{"code","message","request_id","retryable","details"}}`；错误码为闭集（`invalid_argument`、`unauthenticated`、`forbidden`、`not_found`、`conflict`、`idempotency_conflict`、`rate_limited`、`internal`、`unavailable`、`storage_corrupted`、`llm_*`）。Agent 冲突/503 在 `details.reason` 中给出具体原因（`run_active`、`profile_disabled`、`llm_not_configured`、`tools_require_chat_completions`、`capacity`）；P2 资源冲突/422 同样用 `details.reason`（如 `duplicate_key`、`session_state_conflict`、`invalid_storage_state`、`path_not_git_repo`）。
- 认证：`Authorization: Bearer <token>`；登录返回的 token 只出现一次。`/api/agents/*` 与 `agent:` 通道要求管理员。
- P2 公共资源：`/api/resources/*` 使用 camelCase 字段（P0/P1 为 snake_case）；GET 对任意登录用户开放，写/动作仅管理员；`credential` 与 `storageState` 只写不读，响应只含 `credentialSet` 与 storageState hash/形状统计。
- P3 版本：`/api/versions/*` 使用 camelCase 字段，全部登录用户可读写（无 403）；P3 自有 ID 为十进制字符串，P2 资源 ID/用户 ID 为 number；版本事件固定 `channel=version:{id}`，`GET /api/versions/{id}/stream` 服务端锁定通道；错误细分统一放在 `details.reason`（见 OpenAPI `VersionConflict`/`VersionInvalid`/`VersionUnavailable`）。
- P4 代码智能：仓库级 `/api/code/repos/*` 与版本级 `/api/versions/{id}/code/*` 使用 camelCase；codeintel 自有 ID/subjectId 为十进制字符串，`repoId` 等为 number；错误 reason 见 OpenAPI `CodeErrorReason`；`code.changed`/`code.impact_computed` 事件走版本 SSE 通道。
- P6 执行：`/api/versions/{id}/batches*`、`evidence/*`、`execution/sessions` 与 `/api/resources/auth-flows`（admin）；execution ID 为十进制字符串；批次 DTO 兼容 P7 加法字段。
- P7 交付：`/api/versions/{id}/issues`、`/reports`、`/batches/{batchId}/analysis`、mindmap `incremental-plan`/`supplement`、环境 `db-target`（admin）、全局只读 `/api/issues`、`/api/reports`；版本 DTO 新增 `continuePhase/continuedAt`，但 `paused/pause_mode/batch_no` 仍为 P6 snake_case；P7 事件固定 8 个、ID 一律十进制字符串；`db_target` 密码永不回显。
- P8 新增：`GET /api/versions/{id}/exploration-chain`（链路只读聚合）、`GET /api/search`、`GET /api/todos`、`/api/agent-center/{catalog,sessions,quality}`（登录只读）、`/api/versions/{id}/knowledge` 与 `/api/knowledge`（知识草稿/审核/全局已批准）、`/api/code/repos/{repoId}/quality` 与 `.../facts/{factId}/reviews`（质量/事实修正）、`/api/quality/eval-runs`（admin 评测）、`POST /api/admin/gc`（admin，默认 dry-run）；`GET /api/versions/{id}/code/changes` 增加 `batchTimeline`；事件新增 `knowledge.created`/`knowledge.reviewed`，既有 `code.changed`/`report.stale` 语义扩展。限流错误 429 `rate_limited` + `Retry-After`；`GET /api/health` 增加 `rate_limit`/`gc` 只读投影。
- `X-Request-ID` 可传入 `^[A-Za-z0-9_-]{8,128}$`，否则生成 UUID；响应回显。
- 认证写接口支持 `Idempotency-Key`（24h 重放，同 key 不同 body 为 409）。
- CORS 只允许 `EZTEST_WEB_ORIGIN`；SSE 支持 `Last-Event-ID`。Agent 活动事件为 `type=agent.activity`、`channel=agent:{session_id}`，复用 P0 快照与 SSE；9 类 `detail.kind` 见 P1 章节与 OpenAPI `AgentActivityDetail`。

端点分组：

| 分组 | 端点 |
|---|---|
| 认证 | `POST /api/auth/login`、`POST /api/auth/logout`、`GET /api/auth/me` |
| 用户（管理员） | `GET/POST /api/users`、`GET/PATCH /api/users/{id}`、`POST /api/users/{id}/reset-password` |
| LLM（管理员） | `GET/POST /api/admin/llm/configs`、`PATCH/DELETE /api/admin/llm/configs/{id}`、`POST /api/admin/llm/configs/{id}/catalog/refresh`、`POST /api/admin/llm/configs/{id}/test` |
| Agent（管理员） | `GET /api/agents/profiles`、`GET/PATCH /api/agents/profiles/{id}`、`GET /api/agents/tools`、`GET/POST /api/agents/sessions`、`GET/PATCH/DELETE /api/agents/sessions/{id}`、`POST /api/agents/sessions/{id}/messages`、`POST /api/agents/sessions/{id}/stop`、`GET /api/agents/sessions/{id}/messages/{message_id}` |
| 公共资源（读=登录用户，写=管理员） | 系统/环境：`GET/POST /api/resources/systems`、`GET/PATCH/DELETE /api/resources/systems/{id}`、`GET/POST /api/resources/systems/{id}/environments`、`GET/PATCH/DELETE /api/resources/environments/{id}`；应用模型：`GET/POST /api/resources/systems/{id}/app-models`、`POST /api/resources/systems/{id}/app-models/import`、`GET/PATCH/DELETE /api/resources/app-models/{id}`；账号/Access：`GET/POST /api/resources/accounts`、`GET/PATCH/DELETE /api/resources/accounts/{id}`、`GET/POST /api/resources/accounts/{id}/accesses`、`GET/PATCH/DELETE /api/resources/accesses/{id}` |
| Session/接管（读=登录用户，写=管理员） | `GET /api/resources/sessions`、`GET /api/resources/sessions/{id}`、`POST /api/resources/accesses/{id}/sessions`、`POST /api/resources/sessions/{id}/validate`/`confirm`/`invalidate`/`revoke`、`GET /api/resources/sessions/{id}/snapshots`、`POST /api/resources/sessions/{id}/takeovers`、`GET /api/resources/takeovers`、`POST /api/resources/takeovers/{id}/complete`/`cancel` |
| 本地代码仓（读=登录用户，写=管理员） | `GET/POST /api/resources/code-repos`、`GET/PATCH/DELETE /api/resources/code-repos/{id}`、`POST /api/resources/code-repos/{id}/validate` |
| 版本/需求/澄清（登录用户） | 版本：`GET/POST /api/versions`、`GET/PATCH /api/versions/{id}`、`PUT /api/versions/{id}/bindings`、`POST /api/versions/{id}/start-clarification`/`enter-design`；需求：`GET/POST /api/versions/{id}/requirements`、`GET/PATCH/DELETE .../{rid}`、`POST .../{rid}/confirm`/`reopen`、`GET/POST .../{rid}/attachments`、`GET .../attachments/{aid}/content`、`DELETE .../attachments/{aid}`；关系：`GET/POST /api/versions/{id}/relations`、`PATCH/DELETE .../{relId}`；澄清：`GET /api/versions/{id}/clarification`、`POST .../clarification/sessions`、`POST .../sessions/{sid}/complete`、`GET/POST .../clarification/messages`、`POST .../clarification/analyze`/`recon`、`GET/POST .../clarification/questions`、`PATCH .../questions/{qid}`、`GET/POST .../decisions`、`PATCH .../decisions/{did}`；事件：`GET /api/versions/{id}/events`、`GET /api/versions/{id}/stream`（SSE） |
| 代码智能（读=登录用户，索引=管理员） | 仓库级：`GET /api/code/repos`、`POST /api/code/repos/{repoId}/index`（管理员）、`GET .../index-runs/latest`、`GET .../index-runs/{runId}`、`GET .../tree`、`GET .../file`、`GET .../diff`、`GET .../diff/file`、`GET .../facts`、`GET .../search`、`GET .../commits/{sha}`；版本级：`GET /api/versions/{id}/code/changes`、`POST/GET .../analyses`、`GET .../analyses/latest`、`GET .../analyses/{analysisId}`、`PATCH .../analyses/{analysisId}/candidates/{candidateId}`、`GET/POST .../anchors`、`DELETE .../anchors/{anchorId}` |
| P7 交付（登录用户；DB 目标=管理员） | 问题：`GET/POST /api/versions/{id}/issues`、`GET/PATCH .../issues/{issueId}`；报告：`GET/POST /api/versions/{id}/reports`、`GET .../reports/{reportId}`、`POST .../publish`/`regenerate`、`GET .../export`/`diff`；分析：`GET/POST /api/versions/{id}/batches/{batchId}/analysis`、`GET .../analysis/{analysisId}`；增量：`GET /api/versions/{id}/mindmap/incremental-plan`、`PATCH .../mindmap/nodes/{nodeId}/supplement`；全局只读：`GET /api/issues`、`GET /api/reports`；DB 目标（admin）：`GET/PUT/DELETE /api/resources/environments/{id}/db-target`、`POST .../db-target/test` |
| P8 体验/质量（读=登录用户；评测/GC=管理员） | 链路：`GET /api/versions/{id}/exploration-chain`；搜索：`GET /api/search`；待办：`GET /api/todos`；Agent 中心：`GET /api/agent-center/catalog`/`sessions`/`quality`；知识：`GET/POST /api/versions/{id}/knowledge`、`PATCH .../knowledge/{kid}`、`POST .../knowledge/{kid}/review`（审核=admin）、`GET /api/knowledge`；代码质量/事实修正：`GET /api/code/repos/{repoId}/quality`、`POST .../facts/{factId}/reviews`；评测（admin）：`GET/POST /api/quality/eval-runs`、`GET /api/quality/eval-runs/{id}`；GC（admin）：`POST /api/admin/gc` |
| 事件流 | `GET /api/stream/events`、`GET /api/stream`（SSE）、`POST /api/admin/stream/test-events`（需开启测试端点）；`agent:` 通道仅管理员 |
| 存储（管理员） | `POST /api/admin/storage/objects`、`GET /api/admin/storage/objects/{sha256}` |
| 审计（管理员） | `GET /api/admin/audit` |
| 健康/文档 | `GET /healthz`、`GET /api/health`、`GET /api/openapi.yaml` |

## 数据库与迁移

- 迁移文件在 `backend/internal/db/migrations/`，命名 `<context>_<version>_<desc>.sql`，`order.txt` 固定执行顺序。
- 迁移只做加法式 DDL；已应用迁移不修改，只追加。
- API 启动时自动执行：连接后获取 advisory lock、按版本顺序在事务中执行、写入 `schema_migrations`；重复启动幂等（验收脚本会对比两次启动的迁移指纹）。
- 开发库 `eztest`、集成库 `eztest_test` 由 `scripts/db.ps1 init`（或 `.tools/setup-toolchain.ps1`）创建。
- 查看状态：`scripts/db.ps1 status`；停止：`scripts/db.ps1 stop`。

## 测试与验收

在仓库根执行（PowerShell 5.1 兼容）：

```powershell
# 一键本地检查（Go 单元 + Go 集成 + 前端 lint/typecheck/test）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1

# 完整 P0–P8 验收（Go 单元/集成、Agent/Version/Codeintel/Mindmap/Execution/Delivery/Chain/Search/Workspace/Quality、
# 前端 lint/typecheck/test/build、Playwright E2E（含 P4–P8 spec）、迁移重复执行、
# 健康/审计/版本 API 冒烟、交付物与 OpenAPI P1–P8 契约、错误 reason 与事件目录检查、
# P4/P7 fake-LLM 契约、P5/P6 功能与权限/降级子检查、P7/P8 仓库本地静态契约）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1

# 仅静态检查（README/OpenAPI/env/Compose/迁移契约 + P4/P7 fake-LLM + P5/P7/P8 静态契约，
# 不启动数据库/API/Playwright；仍需 Node、frontend/node_modules/yaml 与 git）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1 -StaticOnly

# 仅 P5 mindmap E2E（fake LLM P5 模式；会自行启动/停止服务与 fixture target）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 mindmap

# 仅 E2E（会自行启动/停止 API、Web、fake LLM 上游、fixture target 与 executor，使用 eztest_e2e 库；
# 覆盖 P0–P8：P1 Agent 控制台、P2 Session、P3 版本工作台/澄清/降级、P4 代码理解 5 个 spec、
# P5 mindmap、P6 execution、P7 continuing/问题报告/补全/降级/DB 配置，以及 P8 探索链路/移动导航 spec）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1

# P8 专项
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 exploration-chain
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1 mobile-nav
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -DryRun
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/security-audit.ps1 -SkipLive
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test-backup.ps1

# Compose 静态校验（不需要 Docker 守护进程）
cd frontend
node scripts/validate-compose.mjs
```

分层命令：

| 层 | 命令 |
|---|---|
| Go 单元 | `cd backend; ..\.tools\go\bin\go.exe test ./...` |
| Go 集成 | `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration ./...` |
| Agent 单元 | `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/agent/...` |
| Agent 集成 | `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/agent/...` |
| Version 单元 | `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/version/...` |
| Version 集成 | `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/version/...` |
| Codeintel 单元 | `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/codeintel/...` |
| Codeintel 集成 | `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/codeintel/...` |
| Mindmap 单元 | `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/mindmap/...` |
| Mindmap 集成 | `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/mindmap/...` |
| Execution 单元 | `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/execution/...` |
| Execution 集成 | `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/execution/...` |
| Delivery 单元 | `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/delivery/... ./internal/version/... ./internal/resource/...` |
| Delivery 集成 | `cd backend; $env:EZTEST_TEST_DATABASE_URL=...; ..\.tools\go\bin\go.exe test -tags=integration -count=1 ./internal/delivery/... ./internal/execution/... ./internal/mindmap/...` |
| Delivery 前端 | `cd frontend; pnpm vitest run src/features/delivery src/features/versions src/features/resources src/features/execution` |
| Chain/Search/Workspace/Quality 单元 | `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/chain/... ./internal/search/... ./internal/workspace/... ./internal/quality/...` |
| P8 硬化单元 | `cd backend; ..\.tools\go\bin\go.exe test -count=1 ./internal/platform/leader/... ./internal/execution/... ./internal/version/... ./internal/codeintel/...` |
| P8 前端 | `cd frontend; pnpm vitest run src/features/shell src/features/knowledge src/features/quality src/features/agent-console src/features/execution src/features/versions src/features/code-intel` |
| 评测 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/eval.ps1 -DryRun`（完整门禁见 `eval/README.md`） |
| SLO | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/slo.ps1 -DryRun`；链路专项 `scripts/slo-chain.ps1` |
| 安全/备份 | `scripts/security-audit.ps1 -SkipLive`；`scripts/test-backup.ps1` |
| Mindmap 前端 | `cd frontend; pnpm vitest run src/features/mindmap` |
| P4 fake-LLM 契约 | `node scripts/fake-llm.p4.test.cjs`（需先 `scripts/fixture-codeintel-repo.ps1`） |
| P7 fake-LLM 契约 | `node scripts/fake-llm.p7.test.cjs` |
| 前端单元 | `cd frontend; pnpm test` |
| 前端静态/构建 | `cd frontend; pnpm lint; pnpm typecheck; pnpm build` |
| E2E | `cd frontend; pnpm e2e`（推荐用 `scripts/e2e.ps1` 管理服务、数据库与 fake 上游） |

验收说明：

- 本机没有 Docker，`scripts/acceptance.ps1` 会把“Compose 容器运行”标为 **SKIPPED**；Compose 仅通过 `validate-compose.mjs` 做静态字段校验，不代表已运行验证。P1 真实 Agent LLM、P3 真实 LLM 人工验收、P4/P5/P7 真实 LLM 与 P7 真实 DB 验收同样标为 **SKIPPED**（见下节）。
- 常规测试不访问真实 LLM：Go 测试使用 `httptest` mock（`/v1/chat/completions`、`/v1/responses`、`/v1/models`，可注入 429/5xx/超时/分片）；`scripts/e2e.ps1` 使用 `scripts/fake-llm.cjs`（127.0.0.1:13334）并覆盖 `llm_*`，E2E 零 LLM 费用；P4 的 `scripts/fake-llm.p4.test.cjs` 与 P7 的 `scripts/fake-llm.p7.test.cjs` 同样只启动本地 fake 上游。
- `acceptance.ps1 -StaticOnly` 可在没有 `.env`/数据库/Playwright 的主机上运行：它校验 README/OpenAPI/env/Compose 契约、从 `order.txt` 动态推导 P4–P8 迁移与表清单、校验 P5/P7/P8 交付物与 OpenAPI 契约、解析 OpenAPI YAML，并执行 P4/P7 fake-LLM 测试。仍需要 Node、`frontend/node_modules/yaml` 与 git（P4 fixture）。
- P8 静态检查：`acceptance.ps1 -StaticOnly` 当前覆盖 P8 评测语料/基线（`eval/`）与既有 P1–P7 契约；链路/搜索/待办/Agent 中心/知识/评测/GC 的 OpenAPI 已由本任务同步，但脚本级 P8 契约（如 `scripts/check-docs.ps1`、P8 端点/事件/环境变量断言）仍待后续接入。**视觉断言部分代码已提交但未接线，不在当前静态契约内**。P5/P6 API 子检查在路由未接线时记为 **SKIPPED**；最终判定仍以非 SKIPPED FAIL 数为 0 为准。
- `acceptance.ps1` 额外校验 `docs/openapi/api.yaml` 含 P1 的 `/api/agents/sessions`、`agent.activity`、`reasoning_tokens`，P2 的 `/api/resources/systems`、`/api/resources/sessions`、`/api/resources/code-repos`、`storageState`、`credentialSet`，P3 的 `/api/versions`、`clarification`、`enter-design`、`snowflake`、`version-attachments` 关键词、全部 P3 错误 reason（含字段级 `invalid_context_summary`）、全部版本事件类型（含 `clarification.context_updated`），P4 的 `/api/code/repos/{repoId}/index`、`/api/versions/{versionId}/code/analyses`、全部 P4 path/schema、`CodeErrorReason`（含 `analysis_in_progress`、`anchor_duplicate`、`analysis_queue_full`、`git_unavailable`、`path_not_git_repo`）与 `code.changed`/`code.impact_computed` 事件，P5 的 `/api/versions/{versionId}/mindmap` 23 path/27 operation、8 个 `case.*` 事件、全部 35 个 `MindmapErrorReason`、`AnchorSubjectKind=case` 与 `AppModelNode.source=explored`，以及 P6/P7 的批次/证据/报告/问题/分析/db-target 端点、P7 8 个事件、全部 P7 reason、`code_anchor.subjectKind=issue` 与 P7 前端 testid/导航 phase；同时校验 `.env.example` 的 Agent/P3/P4/P5/P6 变量、Compose 的附件目录与 P4 Playwright spec 是否存在；P1–P7 DoD 与检查项/证据的映射会打印在汇总后。P5/P6 API 子检查在路由未接线时记为 **SKIPPED**；P7 区块全部为仓库本地静态检查（不启动 P7 服务），静态检查与 DoD 映射仍须 PASS。
- 通过条件：所有非 SKIPPED 检查 FAIL 数为 0，脚本退出码为 0。

## 真实 LLM（gated）

只有同时满足以下条件才会产生真实上游调用与费用：

1. `.env` 中 `llm_base_url` / `llm_key` / `llm_model_name` 有效；
2. 显式设置 `EZTEST_RUN_REAL_LLM=1`（P0/P1/P4/P5/P7 脚本）或 `EZTEST_ACCEPTANCE_REAL_LLM=1`（P3 人工验收）；
3. 运行 P0 连通性验收 `scripts/acceptance-llm.ps1`，或 P1 Agent 真实验收 `scripts/acceptance-agent-llm.ps1`（后者验证 `deepseek-flash` 思考模式 + 工具调用 + 终态，并断言 `reasoning_tokens>0`），或 P4 代码智能真实验收 `scripts/acceptance-codeintel-llm.ps1`（真实 Git diff + 证据哈希 + 候选 + unmapped 完整性 + 缓存/force 版本，结果含 `analysis.model`/`promptVersion` provenance 与 degraded 标记，写入 `.tmp/p4-real-acceptance.json`），或 P5 用例脑图真实验收 `scripts/acceptance-mindmap-llm.ps1`（真实设计/评审/数据规划/探索 + 手工人链 + 三格式导出 + 事件 + 降级 + 幂等，结果写入 `.tmp/p5-real-acceptance.json`），或 P7 全自主闭环真实验收 `scripts/acceptance-p7-llm.ps1`（补全/失败分析/问题整理/报告 + 降级场景，gated 且 fail-closed），或按 P3 章节人工执行澄清/分析/侦察验收。

P7 真实 DB 验收：先用 `scripts/fixture-db.ps1` 创建/重置 `eztest_p7_target` 与只读角色（本机 PG），再运行 `scripts/acceptance-p7-db.ps1`（连接/权限失败 → `blocked(environment)`，SQL/只读/截断 → `inconclusive(case)`，结果 `db_result` 证据）；两个脚本都只用于本机验收，不写入版本仓库或生产库。

P4 脚本可选配置：`EZTEST_ACCEPTANCE_P4_REPO`（来源 Git 仓库路径，默认当前仓库；脚本总是把它克隆到 `./.tmp/p4-real-repo` 并在克隆内创建唯一验收分支，来源仓库只读、绝不被写入）、`EZTEST_ACCEPTANCE_P4_BASE`/`EZTEST_ACCEPTANCE_P4_HEAD`（显式 40-hex commit 对，base 必须是 head 的祖先且变更文件 ≤20）、`EZTEST_ACCEPTANCE_API_URL`（复用已在运行的 API；默认重置 `eztest_e2e` 并启动当前源码构建的 API，启动前校验数据库 URL 不能通过 query 参数改目标）。默认要求分析结果至少包含 1 条真实 unmapped；若显式 commit 对全映射，可传 `-AllowNoUnmapped` 记录为 skipped/warning（不得静默通过）。脚本不会把仓库内容、提示词或密钥写入日志。默认 fail-closed：`llm_configured=false` 或任一分析（含复用 API 与 force 路径）`degraded=true` 时失败；显式传 `-AllowDegraded` 才记录 `passed_degraded` 并跳过 AI-only 断言（含 cacheHits 断言）。

P5 脚本可选配置：`EZTEST_ACCEPTANCE_API_URL`（复用已在运行的 API）；默认重置 `eztest_e2e`、构建并启动当前源码的 API，使用 `.tmp/p4-e2e-repo` fixture 仓与 `127.0.0.1:13335` fixture target，导出到 `.tmp/p5-real-exports`。缺 `llm_*` 键、任一 run 非 `done`、缺真实锚点且无 `anchorsDropped` 原因都会失败；只有降级 check 预期 `run=failed reason=ai_unavailable`。`-DryRun` 只校验路径、env 键名、数据库目标与工具链，不重置数据库、不启动 API、不调用 LLM。

普通 `go test`、`pnpm test`、E2E 与 `scripts/acceptance.ps1` 都不会访问真实 LLM；P7 的 `acceptance-p7-db.ps1` 也只连接本机 fixture 目标库。P1/P4/P5/P7 的真实验收与 P3 的人工验收都不进入默认门禁，需人工显式执行。配置也可在管理员登录后于设置页创建/更新，Key 留空表示保留原值。

## 容器部署

见 [`deploy/README.md`](deploy/README.md) 与 [`docs/deployment.md`](docs/deployment.md)。要点：

- `api` 13333、`web` 6868、`postgres:17` 5433（宿主端口）；`executor` 仅内部 13336，不发布宿主端口。
- 需要仓库根 `.env`（gitignored）提供 `POSTGRES_PASSWORD`、`EZTEST_EXECUTOR_TOKEN`、`EZTEST_SECRET_KEYS`、管理员凭据等。
- 两个运行时镜像都以非 root 用户运行；API 镜像已内置 `docs/openapi/api.yaml`，并创建 objects/attachments/evidence/reports/exports 目录。
- P8 持久化卷：`pgdata`、`objects`、`attachments`、`evidence`、`reports`、`exports`；`down -v` 会删除它们。
- Agent 容器默认不启用文件工具（`EZTEST_AGENT_FILE_ROOT` 为空）；需要时把只读目录挂载进容器并设置该变量。`http.get` 默认只允许 localhost，跨主机访问需显式配置 `EZTEST_AGENT_HTTP_ALLOWLIST`。
- P4 代码仓由运维以 **read-only** 方式挂载进 `api` 容器（Compose 已提供注释示例 `./repos:/repos:ro`），P2 的 `localPath` 指向容器内路径（如 `/repos/app`）。P4 不写工作树；挂载可读但不可写是预期配置。
- **本机未安装 Docker**：镜像未构建、Compose 未启动；唯一验证是
  `cd frontend && node scripts/validate-compose.mjs`（解析 YAML，断言服务、端口、健康检查、卷、依赖顺序），以及 `scripts/acceptance.ps1 -StaticOnly` 的 Compose 契约检查。
- TLS/反向代理/HSTS 由部署方负责；SSE 经过反代时必须关闭缓冲（见部署指南）。

## 已知限制（P0–P8）

- 单实例部署：SSE 的订阅与广播在进程内完成，不做多实例广播；重启/多副本会丢失实时连接（客户端可重连回放）。Agent run 也由进程内 runner 调度，重启时 `running` 会标记为 `interrupted`，P1 不做跨实例运行。P8 起执行调度、P7 自主 worker 与每日 GC 维护通过 PostgreSQL advisory lock 单活（每次 tick 仅一个实例执行，其余实例 debug 跳过）；SSE 广播与 Agent runner 仍为单实例。
- 权限：P1 的 `/api/agents/*`、`/agents` 页面与 `agent:` 事件通道均仅管理员可用；P2 的 `/api/resources/*` GET 对任意登录用户开放，写/动作仅管理员（普通用户只读）；P3 的 `/api/versions/*` 与 `/versions` 对全部登录用户开放（无 per-user 授权）。P1–P3 均不做 RBAC。
- Agent 工具：文件工具默认关闭（空 `EZTEST_AGENT_FILE_ROOT`）；`http.get` 只读、host allowlist、最多 3 次重定向、响应截断 1 MB；P1 不提供浏览器/数据库/写操作等业务工具。
- P2 资源：Access 凭据与 Session 状态明文存储，且不写审计/事件流（D28）；Session 过期惰性判定、无后台 GC；代码仓只做本地只读路径/分支校验，P4 提供索引与读取；不提供自动登录、ProbeRecipe 或 Session 执行注入。
- P3 版本域：只实现 `draft → clarifying → designing` 与需求 `draft → clarified`，不提供版本删除/归档、脑图/评审/执行/报告；附件仅落本地文件系统（无 GC/备份，删除 best-effort 可留孤儿文件）；AI 澄清/分析/侦察依赖 P1 Agent 与 LLM，不可用时返回 503 但人工路径可用；单版本最多一个 active 会话与一个运行中 Agent；P3 不写 `audit_logs`。
- P4 代码智能：只做文件级 Git 与 AI 事实，无 parser/符号级调用链/字段级分析、无全仓 AI 分析、无全文检索；只分析变更文件且受 20 文件/64KiB/摘录行数/超时/队列(16)等上限约束；单实例进程内 worker，无取消、无跨进程恢复；仓库必须本地只读可见，不支持线上仓、分支切换或工作树修改；候选只有需求/应用模型（case/issue 归 P5/P6），版本锚点仅 API 可建；事实无人工修正/版本对比 UI；AI 失败降级为 T0 候选但可能产生 `analysis_queue_full`/`analysis_in_progress` 等 429/409；P4 不写 `audit_logs`（`code.read`/`code.analyze` 审计除外）。
- P5 用例脑图：探索仅 HTTP GET/HEAD（无浏览器/JS 执行），依赖环境白名单，可能漏掉纯前端渲染页面；PNG 由前端画布生成，无服务端渲染；导出为 Markdown/Mermaid/OPML（不生成 XMind）；冻结后不可解冻，需求重开后的增量重校验、`affected.cases` 正式接入归 P6/P7（P7 已接入受影响用例/补全）；`impactCases` 只是 P4 候选映射的只读提示；单实例进程内 run worker（重启把 stale run 置 failed）；explore/data_plan 质量受真实 LLM 影响，脚本在非 `done` 时 fail-closed。
- P6 执行：单进程调度；P8 起执行调度/自主 worker/每日 GC 维护通过 PostgreSQL advisory lock 单活（每次 tick 仅一个实例执行，其余实例 debug 跳过），但 SSE 广播与 Agent runner 仍为进程内单实例。db 断言为只读；不做用例级自动重跑（应用失败仅手动 rerun）；证据 GC 已交付但**只清 `EZTEST_EVIDENCE_DIR` 下的执行证据文件**（导出/附件/报告/孤儿文件不自动清理）；执行器不可用时只能查看历史结果/证据/报告，禁止建批；真实 LLM 的分类/暂停结果受模型行为影响，脚本在不符合预期时 fail-closed。
- P7 交付（P8 更新）：`version_change` 表/`code_changed` 已在 P8 落地，`/api/versions/{id}/code/changes` 返回 `batchTimeline`；证据 GC 与报告 stale 联动、多实例 leader 锁已在 P8 落地；报告快照已含 `baseFallbackReason`/`historyRewritten`。仍不提供报告 PDF、问题外部工单、问题重开/合并、跨批次语义去重增强、非 PostgreSQL 目标库；全局问题/报告中心为只读列表；`IssuesTab` 仍较大未拆分（可维护性余项）；`EZTEST_ANALYSIS_MAX_CASES`/`EZTEST_SUPPLEMENT_MAX_CASES` 仍以代码固定上限 20/5 执行，尚未读取；`EZTEST_AUTONOMY_BATCHES_PER_TICK` 仍只按 1–20 校验（未额外钳制）。P7 真实验收脚本的已通过记录见 `acceptance.md`，最终以本分支重跑为准。
- P8 硬化边界：限流是**每实例内存**计数（多副本需按实例预算合计，无分布式限流）；SSE/Agent runner 仍单实例；GC 只自动清理证据文件，且发布/过期报告不参与证据保护（报告快照不含 evidence id）；`EZTEST_CHAIN_MAX_NODES`/`EZTEST_SEARCH_MAX_RESULTS_PER_TYPE`/`EZTEST_TODOS_MAX` 未接线为环境变量；SLO 只有链路专项真实测量 PASS，`scripts/slo.ps1` 全量为 dry-run SKIP（待最终验收）；备份演练未做恢复库应用 smoke（defer Task 35）。
- **视觉断言（D62）部分提交、运行未接线**：迁移（`mindmap_0003_visual.sql`、`execution_0005_visual_evidence.sql`）、mindmap 编辑/审批服务、executor `visual.compare`、前端编辑/审批/查看器、评测语料已提交；但 `cmd/eztest`/`config` 未装配 `Deps.Visual`，因此两条 `/visual` 路由不挂载，execution agent 也没有 `execution.visual_assert` 工具，真实 case run 不会产生 `visual_actual/visual_diff` 证据；`scripts/eval.ps1` 尚未读取视觉语料。当前不要对用户宣称视觉断言可用；合并接线后需同步 OpenAPI/README/用户手册/设计文档/release notes。
- 多实例：只做 advisory lock 单活与执行器硬化；无多实例运维编排（P9 候选）；限流/SSE/Agent runner 不是集群级。
- 登录 token 存 `sessionStorage`：属于单管理员内网工具的 XSS 边界，未做 HttpOnly cookie 会话；CSP 仍为 report-only。
- 无内置 TLS/反向代理/HSTS；生产由部署方负责（见 `docs/deployment.md`）。无多租户/RBAC，管理员角色是唯一权限分层。
- 对象存储只有本地内容寻址实现：无公开 URL、保留期或去重清理；P8 GC 不覆盖对象/附件/导出/报告文件。
- LLM `base_url` 由管理员配置且被信任（P0 管理端测试不做 SSRF 白名单）；P1 Agent 工具调用另有 host allowlist。真实调用受上游可用性/限流/费用影响。
- Compose/镜像仅静态交付，未在本机运行验证；Dockerfile 使用 `golang:1.24.6-alpine`、`node:22-alpine`、`alpine:3.21`，首次构建需要拉取镜像与依赖。
- 迁移只追加、无 down migration；回退依赖备份恢复。丢失 `EZTEST_SECRET_KEYS` 会导致已加密的 LLM Key/headers 不可恢复。
- 页面覆盖：P0 登录、概览、设置；P1 `/agents`（P8 扩展为角色目录/运行观察/质量指标）；P2 `/resources` 与 `/code-repos`；P3 `/versions` 与详情工作台；P4「代码与变更」Tab；P5 全屏脑图；P6 执行类 Tab/暂停恢复/证据/报告；P7 问题/报告 Tab、继续中/增量计划、环境 DB 目标与全局 `/issues`、`/reports`；P8 增加链路 v2、顶栏版本切换/搜索/待办、脑图 Tab、知识库 `/knowledge`、质量中心 `/quality`、移动导航、证据对比、报告 revision 选择器、Agent 中心只读化。
- P7 报告/问题只读引用环境/system/commit 与 `case_run`/证据，不内联 Session storageState/headers/凭据；DB 目标密码明文存储（D48）但任何 API/DTO/事件/审计/日志/证据/报告/prompt 都不回显。

## 许可证

Apache-2.0，见 [`LICENSE`](LICENSE)。
