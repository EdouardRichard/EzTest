# Phase 00 Handoff

> 状态：completed
> 日期：2026-10-31
> 阶段：P0（平台底座与契约）
> 实施分支：`feat/v0.2-p0`（0.1 完整实现保留在 `backup/0.1-dev`）
> 实施 spec：[2026-10-31-eztest-v02-p0-platform-foundation-design.md](../../superpowers/specs/2026-10-31-eztest-v02-p0-platform-foundation-design.md)
> 实施 plan：[2026-10-31-eztest-v02-p0-platform-foundation.md](../../superpowers/plans/2026-10-31-eztest-v02-p0-platform-foundation.md)

## 1. 完成范围

- [x] 严格 4 项启动 env（`EZTEST_DATABASE_URL` / `EZTEST_API_PORT` / `EZTEST_WEB_PORT` / `EZTEST_EXECUTOR_PORT`）；无第 5 项 EzTest env。
- [x] 后端就地重写为 Go 四层模块化单体；`backend/internal` 含 settings/identity/llm/eventing/storage/audit/resource/version 八个 P0 上下文，`cmd/eztest` 唯一组装根。
- [x] DB 设置中心：44 个键、类型/区间校验、幂等 seed、热加载缓存、append-only revision、secret 掩码、admin API/UI。
- [x] 身份/认证：空库种子 `admin/admin123`、登录/登出/me、自助改密、用户管理、单 admin、7 天不透明 Bearer、bcrypt cost 12（SHA-256 预哈希）、审计。
- [x] LLM：`llm_config`/`llm_model` 专用表、默认配置唯一、模型目录 refresh、连通性测试、手工模型、`api_key` 只返回 `api_key_set`。
- [x] 事件/SSE：`event_channel` + 每频道 `seq`、快照 after/limit、SSE Last-Event-ID/after、先订阅再回放、去重、20s 心跳、`stream.reset`、频道白名单。
- [x] 内容账本：`content_object`/`content_ref`、上传 ≤10 MiB、SHA-256、去重、原子写、下载校验、路径不暴露。
- [x] 审计：`audit_log`、登录/设置/资源/版本/内容关键动作、admin 查询。
- [x] 资源域：SUT/环境/页面/API/目标库/测试账号/代码仓 CRUD、页面/API JSON 导入、默认环境唯一、密码只回 `password_set`、删除前版本绑定守卫。
- [x] 版本：创建多 SUT 绑定（环境/页面/API scope/库/账号/代码仓）、列表、详情嵌套视图、标题/自动设计开关更新；P0 只写 `initial`。
- [x] 前端就地重写：登录、8 项全局壳、版本列表/多 SUT 创建向导/8 Tab 详情壳、资源四 Tab、设置四 Tab、P1+ 占位页。
- [x] 契约：`contracts/openapi.yaml`（43 paths / 66 operations）、`events.md`、`db-schema.md`、`migrations-order.md`、`settings.md` 冻结为 P0。
- [x] 测试/演示：Go 单测+集成、Vitest、production build、Playwright 旅程、API 冒烟、截图。
- [x] 未修改 `docs/version-V0.2` 设计文档；P0 决策记录在 spec §13（P0-D1..P0-D19）。

## 2. 契约变更

| 类型 | 变更 | 是否 frozen | 影响阶段 |
|---|---|---|---|
| API | `contracts/openapi.yaml`：auth/users/settings/LLM/audit/content/events/stream/resources/versions/health 全部 P0 端点；ID 字符串、错误/分页 envelope、Bearer | `frozen: P0` | P1–P8 |
| 事件 | `contracts/events.md`：`event_channel` + `event(channel,seq)` + envelope、快照/SSE/游标/stream.reset/20s 心跳、P0 事件目录、单实例 Hub | `frozen: P0` | P1–P8 |
| 表 | 8 个迁移、21 张表：settings 2、identity 2、llm 2、eventing 2、storage 2、audit 1、resource 7、version 3 | `frozen: P0` | P1–P8 |
| 迁移顺序 | settings → identity → llm → eventing → storage → audit → resource → version；`order.txt` 为唯一顺序，SHA-256 checksum、advisory lock、只追加 | `frozen: P0` | P1–P8 |
| 设置 | `contracts/settings.md`：4 env + 44 键（type/default/category/restart） | `frozen: P0` | P1–P8 |
| 任务/工具 | `task-types.md`、`agent-profiles-tools.md` 未创建；按 contracts/README 从 P1 开始 | 不适用 | P1 |

- **P0 契约修正**：`ResetPasswordRequest` 由 `password` 对齐为实现 `new_password`；`UpdateSettingRequest.value` 改为 required（P0 目录无 secret 键，后端拒绝 null）；`/api/resources/catalog` 对齐为 `{suts, repositories}`。
- **已知契约漂移（不阻塞 P0 UI，P1 首次改契约时收口）**：
  1. `GET /api/admin/llm/configs` 与 `/models` 后端返回 ListEnvelope（多 `next_cursor`/`has_more`），openapi 写成 plain `{data}`；客户端 tolerable。
  2. LLM create/add-model 后端返回 200（契约 201）、delete 返回 200+body（契约 204）；UI 不依赖状态码。
  3. 迁移 `idx_event_created_at (created_at)` 与 spec §8.1 的 `(channel, created_at)` 不一致；已在 `db-schema.md` 记录实际索引；如需严格对齐，P1 追加 `eventing_0002` 迁移，不改已应用迁移。
- **破坏性变更**：无（P0 全新 schema；0.1 数据不迁移，旧库只读归档）。

## 3. 测试与验收证据

| 证据 | 命令 | 结果 |
|---|---|---|
| 全量门禁 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1` | Go 单元+集成 63 个包次 `ok`、0 `FAIL`；前端 `tsc --noEmit` 0、Biome 0、Vitest 30 文件 / 243 tests passed；日志 `.tmp/p0/final-gate.log` |
| 生产构建 | `cd frontend; pnpm build` | `Compiled successfully`，12 条路由（`/`、`/login`、`/versions`、`/versions/[id]`、`/resources`、`/settings`、5 个 P1 占位）；日志 `.tmp/p0/web-build.log` |
| E2E | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/p0-e2e.ps1` | 1 passed（login → versions → detail → resources → settings）；日志 `.tmp/p0-acceptance/e2e.log` |
| 后端 E2E 集成 | `EZTEST_TEST_DATABASE_URL=... go test -tags=integration ./internal/server/...` | `TestP0EndToEndFlow` OK：login/settings/resources/version/事件快照/内容/审计/权限/删除守卫 |
| 契约 YAML | Node + `frontend/node_modules/yaml` | OpenAPI 3.0.3，43 paths 可解析 |
| API 手工冒烟 | `http://127.0.0.1:13333`，admin/admin123 | 44 settings；创建 SUT/环境/页面/API/库/账号/仓库；创建 2 SUT 版本（Smoke SUT + Smoke Payment），详情 bindings=2、`version.created.sut_count=2`；SUT 删除 409；内容上传/下载 SHA 一致；fake LLM test/refresh 2 models |
| 截图 | `.tmp/p0-acceptance/screenshots/` | `login.png`、`versions.png`、`version-detail.png`、`version-detail-multi-sut.png`（2 SUT 绑定）、`resources.png`、`settings.png`（已人工检查布局/中文/空状态） |

**演示路径（当前本机栈）**
- API：`http://127.0.0.1:13333`；Web：`http://localhost:6868`
- 管理员：`admin` / `admin123`（首启默认；用户登录后自行改密）
- 开发库：`eztest_v02`（PostgreSQL `localhost:5433`）；集成测试库：`eztest_v02_test`
- 启动：`scripts/dev-api.ps1` + `scripts/dev-web.ps1`；或在 `backend/` 使用 `.tmp/p0/eztest.exe`（4 env）
- 停止：结束 13333/6868 对应进程；executor 13336 仍在运行（P0 不使用）
- 注意：开发库含 P0 冒烟数据（Smoke SUT/Smoke Version）；重新演示可清空 `eztest_v02` 后启动，会自动迁移+seed。

## 4. 已知缺口/偏差

| 缺口/偏差 | 影响 | 是否阻塞下一阶段 | 决策引用 |
|---|---|---|---|
| 固定默认管理员 admin/admin123，无强制改密 | 开发/测试信任模型；对外部署必须首启改密 | 否 | P0-D1 |
| 无登录失败限流/验证码 | 受控环境可接受；P1 或硬化阶段补 | 否 | spec §4.2 |
| LLM `api_key` 明文入库、API 掩码 | 按 D11/D51 信任模型；无第 5 个 env 可用主密钥 | 否 | P0-D3 |
| 事件 Hub 进程内、单实例 | P0 单实例假设；P7 再做 PG LISTEN/NOTIFY/多副本 | 否 | P0-D6/spec §8 |
| 未实现通用 `Idempotency-Key` | P0 创建资源/版本未使用；后续波次/确认/导出再接入 | 否 | spec §7.1 |
| 契约漂移（LLM list envelope/状态码、event index） | UI 不依赖；P1 首次改契约时收口 | 否 | §2 |
| 0.1 `scripts/e2e.ps1`、`scripts/acceptance.ps1`、`scripts/fake-llm.p*.test.cjs` 与新 fake LLM 不兼容 | 不要在本分支运行这些旧脚本；P0 用 `scripts/test.ps1` 与 `scripts/p0-e2e.ps1` | 否 | T16 记录 |
| 资源 UI 未按角色隐藏写操作按钮 | 普通用户点击会收到后端 403 并内联展示；不是安全漏洞 | 否 | T14 记录 |
| 版本详情对已删除引用资源严格 404 | 正常流程由删除守卫阻止；绕过守卫不可达 | 否 | T10 记录 |
| 需求/设计/执行/交付/链路/知识/代码智能/视觉/质量/搜索/待办/Agent 中心未实现 | P1–P6 范围 | 否 | phase-01..06 |
| 工作区仍有 0.1 遗留未提交文件（`docs/development.md`、`docs/startup.md`、`scripts/dev-all.ps1`、生成的 `frontend/next-env.d.ts`） | 不属于 P0 提交；P1 开始前确认清理/归档 | 否 | 本次记录 |

**P0 实现决策**：完整列表见 spec §13：
- P0-D1 固定默认管理员；P0-D2 就地重写；P0-D3 LLM 专用表+明文掩码；P0-D4 雪花 node 存 DB；P0-D5 CORS 由 web port 派生；P0-D6 event_channel 每频道 seq；P0-D7 冻结设置目录；P0-D8 executor URL 由 env 派生；P0-D9 版本只写 initial；P0-D10 删除守卫；P0-D11 LLM 仅配置；P0-D12 `NEXT_PUBLIC_API_BASE_URL` 构建期配置；P0-D13 代码仓任意登录用户 CRUD；P0-D14 身份表/token 7d；P0-D15 导入 JSON；P0-D16 全员可见版本；P0-D17 0.2 设计权威；P0-D18 自助改密；P0-D19 完整设置目录。

## 5. 下一阶段输入（P1）

**必须读取**
- `docs/version-V0.2/contracts/{openapi.yaml,events.md,db-schema.md,migrations-order.md,settings.md}`（frozen P0）
- 本 handoff + `docs/version-V0.2/phases/phase-01-runtime-clarification.md`
- `docs/version-V0.2/architecture-design.md` §5–§9；`high-level-design.md` §2–§6

**可依赖的接口/表**
- 组装：`backend/cmd/eztest/main.go` 已按 settings→ids→audit/eventing→identity→llm→storage→resource→version 装配；`server.New(server.Deps)` 注册全部 P0 路由。
- 跨上下文端口：
  - `auditapp.Recorder`（`Event{Action,TargetKind,TargetID,RequestID,Metadata}`）
  - `eventingapp.Publisher`（`Event{Channel,Type,Scope,Payload,ActorID,RequestID}`；channel `system|version:{id}|user:{id}`）
  - `settingsapp.Service` 的 `String/Int/Bool/Get/Update` 与 44 键目录
  - `resourceapp.CatalogReader` / `UsageChecker`
  - `identityapp.Service.EnsureSeedAdmin/Authenticate/...`；`platform.Actor`
  - `storageapp.Service.Upload/Open/AddRef/ListRefs/SoftDeleteRef`
- 表：8 个迁移、21 张；P1 只追加，不改 P0 迁移。
- 事件：P0 事件目录见 `contracts/events.md`；P1 新增事件只能追加。
- 前端：`src/lib/api-client.ts`、`sse-client.ts`、`auth-storage.ts`；`features/{auth,shell,versions,resources,settings}`；UI 原语在 `components/ui`；路由壳在 `src/app/(app)`。
- 测试：`scripts/test.ps1`、`scripts/p0-e2e.ps1`、`backend/internal/testutil.NewDB`（集成 schema 隔离）。

**P1 前置检查**
1. 4 env 仍严格 4 项；P1 新配置只进 DB settings。
2. 运行 `scripts/test.ps1` 与 `tests` 全绿后再开始。
3. 若需改 P0 frozen 契约：先停止、列影响、更新 contracts + 决策记录，再实现；禁止代码先行。
4. 不要恢复 0.1 的 8 状态版本机/人工批次/整版冻结；版本状态只是投影。
5. P1 新增 work_item/agent profile/context packet 表时，先写迁移 + contracts 增量，再写运行时。

## 6. 风险

1. **资源模型耦合**：`sut/environment/page/api/database/account/repository` 已冻结，P1–P6 都会消费；字段变更成本高，只能追加/兼容。
2. **固定默认凭据**：一旦对外暴露必须先改密；P0 不做强制改密/限流。
3. **事件单实例**：P1 的 reconciler/SSE 依赖单实例 Hub；P7 前不得承诺多副本实时。
4. **契约漂移**：LLM list/status 与 event index 偏小但会在 P1 写新契约时放大；P1 开始先收口。
5. **0.1 旧脚本**：`scripts/e2e.ps1`/`acceptance.ps1` 不能在本分支运行；P1 需要新的 fake LLM/验收脚本时基于 `scripts/p0-e2e.ps1` 扩展。
6. **演示数据**：开发库 `eztest_v02` 含冒烟数据；长期演示前清库重跑迁移/seed，避免名称冲突。
7. **P1 范围膨胀**：任务板/租约/reconciler/context/Agent runtime/对话/需求版本同时落地，必须先冻结 P1 契约再实现；不要提前改 P0 表。
