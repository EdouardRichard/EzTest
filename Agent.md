# Agent.md — EzTest 文档查询与权威约定

> 本文件是 EzTest 的 **Agent 入口契约**：只回答"该查哪些文档、哪些是权威、如何引用、冲突怎么办"。
> **不包含**：项目进度、阶段划分、任务/分支状态、设计细节或设计决策。
> 开始任何任务前，按本文件查询相关文档；**不要把本文件当作设计依据**。

---

## 1. 查询流程（每次任务开始前）

1. 按 §4 文档地图与任务查询指引，直接打开对应文档；设计判断以 `docs/design/system-blueprint.md` 为准。
2. 按 §2 给出"阅读依据"（可核验的文件 + 章节/行号，明确未读/未找到项）。
3. 若发现文档冲突、缺失或不可访问：按 §3 停止并报告，不得臆造或自行裁决。

---

## 2. 阅读依据（禁止形式主义）

本文件不要求任何"已阅读"声明；要求**实际查阅 + 可核验引用**：

1. **实际打开**相关文档，而不是凭记忆或猜测。
2. 在回复中给出**阅读依据**，例如：

```text
阅读依据：
- docs/design/system-blueprint.md §7（代码智能）
- docs/design/code-intelligence.md §5.3（AI 事实证据校验）
未读/未找到：docs/design/xxx.md（不存在/未提供）
```

3. **禁止**：
   - 用"已阅读/我已确认"等空泛声明代替具体引用；
   - 编造文件内容、章节、行号；
   - 未实际查阅却声称读过；
   - 文档不存在/不可访问时假装读过。
4. 文档缺失或不可访问：明确报告"未读/不可访问"，停止相关工作，不得编造。

---

## 3. 权威顺序与冲突处理

1. **`docs/design/system-blueprint.md` = 唯一设计权威**（设计事实、决策、原则、边界均以此为准）。
2. **详细设计文档**（`code-intelligence.md` / `frontend-design.md` / `auth-session-model.md`）= 主蓝图对应领域的展开；**冲突时以主蓝图为准**。
3. **研究/参考/计划/归档类文档**（Pre-Research、计划排期、历史归档、旧版本）= **非设计权威**，仅作参考。
4. 发现冲突：
   - 停止实现，在回复中列出冲突点（文件 + 章节/行号）；
   - 以主蓝图修正详细设计；
   - 若主蓝图内部冲突或疑似设计缺陷：报告负责人，不自行裁决。
5. 发现文档缺失/不可访问：报告并停止；不要假设文档内容存在。

---

## 4. 文档地图与任务查询

| 文档 | 定位 | 权威性 | 何时查 |
|---|---|---|---|
| `docs/design/system-blueprint.md` | 产品/范围/架构/领域模型/Agent/生命周期/代码/执行/数据/API/前端/非功能/风险/决策 | **唯一设计权威** | 任何设计判断/实现前 |
| `docs/design/code-intelligence.md` | 代码智能详细设计 | 详细设计（从属于主蓝图） | 代码理解/影响分析/增量执行 |
| `docs/design/frontend-design.md` | 前端详细设计 | 详细设计（从属于主蓝图） | 前端/交互/UI |
| `docs/design/auth-session-model.md` | 账号/会话详细设计 | 详细设计（从属于主蓝图） | 资源/执行前置/账号安全 |
| `docs/design/system-overview.md` | 早期版本（已归档） | 非权威；不得用于设计决策 | 仅查历史 |
| `docs/superpowers/specs/` | 阶段设计规格（P0 起） | 阶段实现依据（从属于主蓝图；冲突以主蓝图为准） | 实现某 Phase 前 |
| `docs/Pre-Research/`（ARTEX 研究） | 研究/借鉴参考 | 参考（非设计权威）；不得复制源码 | 需要 ARTEX 设计/实现参考时 |
| 计划/排期/进度类文档（若有） | 实施规划 | **非设计权威**；不得作为设计依据、验收约束或阶段限制 | 仅排期参考 |
| `LICENSE` + 本地 ARTEX 参考仓库 | 许可与参考实现 | Apache-2.0 / AGPL-3.0 合规边界 | 引入依赖、参考 ARTEX 实现时 |

> 本表即设计文档地图；新增/删除设计文档时更新本表，不另设 README 索引。
>
> **阶段编号约定**：`P0–P8` 仅指 `implementation-phases.md` 的实施阶段；其他详细设计的局部里程碑使用独立前缀（`CI-Mx` 代码智能、`AS-Mx` 账号会话），不得复用 `P0–P8`。前端交付归属见 `implementation-phases.md` §2.9。

**按任务查询指引**（章节号以文档实际为准）：

| 任务类型 | 先查 |
|---|---|
| 产品定位/范围/决策 | 主蓝图 §1–§3 |
| 总体架构/模块边界 | 主蓝图 §4 |
| 领域模型/生命周期/批次 | 主蓝图 §5 |
| Agent 体系 | 主蓝图 §6 |
| 代码智能 | 主蓝图 §7 + `code-intelligence.md` |
| 执行引擎/证据 | 主蓝图 §8 |
| 账号/会话 | 主蓝图 §9 + `auth-session-model.md` |
| 数据/API | 主蓝图 §10–§11 |
| 前端 | 主蓝图 §12 + `frontend-design.md` |
| 非功能/质量/风险 | 主蓝图 §13–§16 |
| ARTEX 参考 | 主蓝图附录 + `docs/Pre-Research/` |
| 阶段规格/实现依据 | 主蓝图 §15 + `implementation-phases.md` + `docs/superpowers/specs/` 对应规格 |
| 排期/计划 | 计划类文档（非设计权威） |

---

## 5. 实现与变更的查询规则（只列流程）

1. **没有设计依据不实现**：不凭空发明接口、DDL、状态机、交互；先查文档并引用依据。
2. **相似功能先查 ARTEX 参考实现**：需要参考实现时，先查阅本地 ARTEX 源码（参考起点见主蓝图附录/Pre-Research），并在实现说明中记录参考 `file:line`、对齐点与刻意差异；ARTEX 无对应时明确说明"无对应"。
   - **参考 ≠ 复制**：EzTest 为 Apache-2.0，ARTEX 为 AGPL-3.0，禁止复制/大段搬运/派生 ARTEX 源码；只允许阅读、理解、重写实现；确需复用代码须先由负责人做许可证决策。
3. **设计变更流程**：在主蓝图决策记录新增 `D#` → 更新受影响章节 → 同步受影响详细设计 → 若文档增删/地图变化，更新本文件 §4 → 评审后再实现；禁止"代码先行、文档后补"。
4. **本文件自身维护**：文档地图由本文件（§4）统一维护；仅当文档结构、权威关系、查询/阅读规则变化时更新；**不在本文件记录项目进度、阶段、任务或分支状态**。

---

## 6. 仓库结构与代码边界规则

> 本节是**可执行的目录与导入约束**，用于防止后续阶段（含 AI）随意新增目录、跨层引用或复制参考实现。结构变更必须先改本节与主蓝图 `D26`。

### 6.1 目录结构（唯一形态）

```text
EzTest/
├─ backend/
│  ├─ go.mod
│  ├─ cmd/eztest/main.go              # 唯一组装根（composition root）
│  └─ internal/
│     ├─ config/                      # env 解析/校验，不放业务逻辑
│     ├─ db/                          # pgx pool、事务 helper、迁移执行器
│     │  └─ migrations/               # <context>_<version>_<desc>.sql
│     ├─ server/                      # 路由装配、中间件链，不放业务逻辑
│     ├─ platform/                    # 共享内核：clock/id/errors/pagination/validation/crypto
│     │  └─ id/snowflake.go           # 雪花 ID 生成器（D29：41bit 毫秒 + 10bit 节点 + 12bit 序列）
│     ├─ identity/                    # 限界上下文：用户/认证
│     │  ├─ domain/                   # 实体、值对象、仓储接口、领域错误
│     │  ├─ application/              # 用例/命令/查询
│     │  ├─ infrastructure/           # postgres repo、bcrypt、token 生成
│     │  └─ interfaces/httpapi/       # handler、DTO、路由
│     ├─ eventing/                    # 限界上下文：事件/SSE
│     ├─ llm/                         # 限界上下文：LLM 配置/网关/模型目录
│     ├─ storage/                     # 限界上下文：对象存储
│     ├─ audit/                       # 限界上下文：审计
│     ├─ agent/                       # 限界上下文：Agent 运行时（P1）
│        ├─ domain/                   # profile/session/run/message/activity/tool
│        ├─ application/              # run 循环、上下文组装、工具管线、端口
│        ├─ infrastructure/           # {postgres,llm,eventing,audit,tools}
│        └─ interfaces/httpapi/       # /api/agents/* handler、DTO
│     ├─ resource/                    # 限界上下文：公共资源与身份（P2）
│     │  ├─ domain/                   # company/system/environment/appmodel/account/access/session/snapshot/lease/takeover/coderepo
│     │  ├─ application/              # catalog/access/session/coderepo 服务、ports
│     │  ├─ infrastructure/           # {postgres,httpcheck,gitexec}
│     │  └─ interfaces/httpapi/       # /api/resources/* handler、DTO
│     ├─ version/                     # 限界上下文：版本/多需求/澄清（P3，D31）
│        ├─ domain/                   # version/requirement/relation/attachment/clarification/decision
│        ├─ application/              # version/requirement/relation/clarification/attachment 服务、agent_tool、ports
│        ├─ infrastructure/           # {postgres,filestore,gitread,agent,eventing,resource}
│        └─ interfaces/httpapi/       # /api/versions/* handler、DTO
│     ├─ codeintel/                   # 限界上下文：代码智能（P4，D32/D33）
│        ├─ domain/                   # entity/edge/change_set/analysis/fact_version/anchor/evidence/contract/limits
│        ├─ application/              # index/analysis/impact/anchor 服务、run_worker、prompt/parser、agent_tool、ports
│        ├─ infrastructure/           # {gitx,postgres,llm,eventing,resource,version,audit}
│        └─ interfaces/httpapi/       # /api/code/*、/api/versions/{id}/code/* handler、DTO
│     └─ mindmap/                     # 限界上下文：用例脑图与评审（P5，D35）
│        ├─ domain/                   # node/step/assertion/source/review/finding/baseline/run/export、规则、错误、提示词常量
│        ├─ application/              # graph/materialize/review/baseline/export/explore/run 服务、ports
│        ├─ infrastructure/           # {postgres,filestore,http,eventing,version,resource,codeintel,agent,audit}
│        └─ interfaces/httpapi/       # /api/versions/{versionId}/mindmap/* handler、DTO
├─ frontend/
│  ├─ src/app/                        # Next.js App Router，只做页面组装
│  │  ├─ (app)/agents/                # /agents 路由，挂载 agent-console
│  │  └─ (app)/versions/              # /versions、/versions/[id] 与 /versions/[id]/mindmap 路由，挂载 versions/mindmap
│  ├─ src/features/{auth,users,llm-settings,sse-test,shell,agent-console,versions,code-intel,mindmap}/
│  ├─ src/components/ui/              # 共享 UI 原语
│  ├─ src/lib/                        # api-client / sse-client / auth-storage / utils
│  ├─ src/hooks/  src/navigation/  src/styles/
│  └─ package.json
├─ deploy/                            # Dockerfile.api / Dockerfile.web / docker-compose.yml
├─ scripts/                           # dev/db/test/e2e/验收脚本
│  ├─ fake-llm.cjs                    # E2E 确定性 LLM 上游
│  ├─ e2e.ps1                         # Playwright E2E（fake 上游）
│  └─ acceptance-agent-llm.ps1        # gated 真实 LLM 验收
├─ docs/openapi/api.yaml              # OpenAPI 契约
├─ docs/superpowers/specs/            # 阶段设计规格（P0 起）
├─ .env.example  .gitignore
└─ .tools/                            # 便携 Go/PostgreSQL（gitignored）
```

每个限界上下文**必须**四层齐全：`domain/`、`application/`、`infrastructure/`、`interfaces/httpapi/`。上下文名使用小写单数（`identity`、`eventing`、`llm`、`storage`、`audit`、`agent`、`resource`、`version`、`codeintel`、`mindmap`），不得新建 `utils/`、`common/`、`helpers/` 之类杂物目录；共享纯逻辑放 `internal/platform/`。

P1 新增的 `agent` 上下文位于 `backend/internal/agent/`：`infrastructure/` 含 `postgres`、`llm`、`eventing`、`audit`、`tools` 适配，`interfaces/httpapi/` 暴露 `/api/agents/*`。P3 兼容扩展：Agent 会话新增可选 `activity_channel`（澄清会话为 `version:{id}`；默认回退 `agent:{session_id}` 不变），run 支持文本 + `image_ref` 多模态输入。

P2 新增的 `resource` 上下文位于 `backend/internal/resource/`：承载公共资源与身份（D28），`infrastructure/` 含 `postgres`、`httpcheck`、`gitexec` 适配，`interfaces/httpapi/` 暴露 `/api/resources/*`。

P3 新增的 `version` 上下文位于 `backend/internal/version/`：承载版本/多需求/澄清（D31），四层齐全；`infrastructure/` 含 `postgres`、`filestore`、`gitread`、`agent`、`eventing`、`resource` 适配，`interfaces/httpapi/` 暴露 `/api/versions/*`。主键由 `backend/internal/platform/id/snowflake.go` 生成（D29），需求附件走文件系统（D30）；前端 feature 位于 `frontend/src/features/versions/`。

P4 新增的 `codeintel` 上下文位于 `backend/internal/codeintel/`：承载代码理解与影响分析（D32/D33），四层齐全；`infrastructure/` 含 `gitx`、`postgres`、`llm`、`eventing`、`resource`、`version`、`audit` 适配，`interfaces/httpapi/` 暴露仓库级 `/api/code/*` 与版本级 `/api/versions/{id}/code/*`。新增 9 表（雪花 `BIGINT` 主键；`repo_id`/`version_id`/`requirement_id`/`app_model_id` 均为逻辑引用、不建跨上下文 FK）；分析/索引为异步 run（202 + 轮询 + `version:{id}` SSE），复用 P0 LLM 网关，不新增 Agent profile（仅注册 `code_*` 只读工具薄壳，P3 `code.tree`/`code.read` 契约不变）；前端 feature 位于 `frontend/src/features/code-intel/`。

P5 新增的 `mindmap` 上下文位于 `backend/internal/mindmap/`：承载用例脑图与评审（D35），四层齐全；`infrastructure/` 含 `postgres`（9 表仓储）、`filestore`（导出物）、`http`（应用探索 HTTP 客户端）、`eventing`、`version`（状态端口适配）、`resource`（应用模型/环境/Session 端口适配）、`codeintel`（事实/锚点/影响端口适配）、`agent`（run 启动/查询端口适配）、`audit` 适配，`interfaces/httpapi/` 暴露 `/api/versions/{versionId}/mindmap/*`。新增 9 张 `case_*` 表：`case_node`/`case_step`/`case_assertion`/`case_source`/`case_review`/`case_review_finding`/`case_baseline`/`case_design_run`/`case_export`（雪花 `BIGINT` 主键 D29；表名/索引名遵循 §6.9/D34，审计列沿用 `created_at/updated_at` 的 D34 显式例外）。导出物走文件系统（D30），`case` 锚点经 codeintel `AnchorService`（D37）；前端 feature 位于 `frontend/src/features/mindmap/`，页面路由 `/versions/[id]/mindmap`。

### 6.2 导入方向（Go，强制）

1. `domain` 只依赖标准库与 `platform` 的纯类型；**禁止**导入 `application` / `infrastructure` / `interfaces`，禁止导入 pgx、HTTP、LLM SDK。
2. `application` 可导入同上下文 `domain` 与 `platform`；**禁止**导入 `infrastructure` / `interfaces`。
3. `infrastructure` 实现 `domain` 定义的接口；可导入同上下文 `domain` / `application` 的端口定义；**禁止**被 `domain` / `application` 反向导入。
4. `interfaces/httpapi` 只调用 `application`；DTO 与领域对象分离；**禁止**直接写 SQL 或调用 LLM SDK。
5. 跨上下文只能通过对方 `application` 接口或事件协作；**禁止**导入对方 `infrastructure` / `interfaces`，禁止跨上下文共享可变状态。
6. `cmd/eztest` 是唯一组装根；`server` 只做路由与中间件装配；`config` 只做配置解析；`db` 只做连接/事务/迁移。
7. 测试与被测代码同包/同目录（`*_test.go`）；集成测试可放 `internal/<context>/integration/`，不得把测试辅助代码塞进生产包。

### 6.3 迁移规则

- 迁移文件集中在 `backend/internal/db/migrations/`，命名 `<context>_<version>_<desc>.sql`（如 `identity_0001_users.sql`）。
- 迁移只做加法式 DDL；已应用迁移**禁止修改**，只能追加新版本。
- 启动时获取 `pg_advisory_lock`，按版本顺序事务执行，写 `schema_migrations`；重复执行无副作用。
- 一个迁移文件只属于一个上下文；跨上下文外键需显式评审。
- 种子数据只允许幂等初始化（管理员、默认 LLM 配置），不得写入业务样例数据。
- 表名/字段名/索引名遵循 §6.9（D34）；历史表改名走新增迁移，不改已应用迁移与既有字段/约束/索引名。

### 6.4 前端规则

- `src/app/` 只做路由、布局与页面组装；业务状态与交互逻辑放 `src/features/<feature>/`。
- 所有 HTTP 请求必须走 `src/lib/api-client.ts`（统一错误信封、`X-Request-ID`、Bearer Token）；禁止在组件里直接 `fetch`。
- SSE 只能走 `src/lib/sse-client.ts`（fetch 流、`Last-Event-ID`、重连状态）；禁止在 feature 内另写 EventSource/轮询。
- 共享 UI 原语放 `src/components/ui/`；跨 feature 的导航放 `src/navigation/`；主题/全局样式放 `src/styles/`。
- 不允许新建 `src/utils/` 大杂烩；纯函数放 `src/lib/` 并按领域命名。

### 6.5 新上下文/新模块接入

新增限界上下文时，必须同时：

1. 建齐四层目录与最小空实现；
2. 在 `cmd/eztest` 注册依赖；
3. 在 `internal/db/migrations/` 增加该上下文迁移；
4. 更新本节 6.1 目录树与 `docs/design/system-blueprint.md` 模块边界（如需）；
5. 若新增前端页面，在 `frontend/src/features/` 建 feature 并在 `src/app/` 只做路由组装。

### 6.6 ARTEX 参考边界

- 只读参考 `D:\Project_new\ARTEX`，**禁止复制/大段搬运/派生源码**（AGPL-3.0；EzTest 为 Apache-2.0）。
- 需要参考实现时，在实现说明中记录参考 `file:line`、对齐点与刻意差异；ARTEX 无对应时明确写“无对应”。
- 目录形态参考 ARTEX 的按能力分包与 `cmd/config/db/server/web` 组织方式，但 DDD 分层与导入规则以本节为准。

### 6.7 主键与 ID 规范（全项目）

1. 自 P3 起，所有**新增**实体/表的主键统一使用雪花 ID（64-bit：41bit 毫秒 + 10bit 节点 + 12bit 序列），由 `backend/internal/platform/id` 在应用层生成；**禁止新增 `BIGSERIAL`/`SERIAL`/数据库自增主键**。
2. 同一实体在 DB、Go、JSON、事件与工具参数中使用同一 int64 语义；64-bit ID 在 JSON/工具参数中一律为十进制字符串（避免 JS 精度丢失），URL 路径参数为十进制字符串。
3. 雪花节点 ID 由 `EZTEST_SNOWFLAKE_NODE_ID` 配置（0–1023，单实例默认 1）；多实例部署必须保证节点唯一。
4. 既有 P0–P2 的 `BIGSERIAL` 表**不回溯改造**；如需统一，必须单独立项迁移并记录 D#；迁移完成前不得混用同一实体的 ID 类型。
5. 跨上下文引用与主键同类型（BIGINT），仍按 §6.2 只做逻辑引用、不建 FK。

### 6.8 文件存储规范（全项目）

1. 业务附件与用户文件（需求附件、证据、截图、导出等）统一存**文件系统**，由所属上下文提供存储适配；**禁止把二进制（含 base64/data URL）写入 PostgreSQL**。
2. 文件存储必须：根目录可配置、路径由应用生成并做根内校验、拒绝 `..`/绝对路径/symlink 逃逸、文件名清洗、临时文件 + 原子 rename、文件 0600/目录 0700、大小与数量上限。
3. 新功能不得新增对 P0 `storage` 内容寻址对象存储的依赖；该上下文冻结为既有能力/历史兼容，确需复用须在阶段规格中显式记录理由与 D#。
4. 删除采用“DB 元数据 + best-effort 文件删除”；孤儿文件由 GC/保留策略处理，不阻塞业务。
5. 下载/读取必须鉴权，不对外暴露内部存储路径；文件内容视为不可信数据。

### 6.9 数据库命名规约（D34）

1. **表名**：小写字母/数字/下划线；**单数**；禁止保留字；推荐 `业务名称_表的作用`（如 `case_review_finding`）；禁止 `tbl_`/`tb_` 前缀。
2. **字段名**：小写+下划线；布尔用 `is_` 前缀；禁止保留字；P5 **不改**任何既有字段名。
3. **主键**：`id BIGINT`（雪花，D29），不做自增、不做 unsigned 语义（PostgreSQL 无 unsigned）；每表必须有主键。
4. **审计字段**：P5 起新表沿用 `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`、`updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`（对阿里“id/gmt_create/gmt_modified 三字段”的**显式例外**，理由：与本仓库既有字段/Go 映射一致，避免两套时间字段语义）。
5. **索引命名**：`pk_<表名>`（主键约束）、`uk_<表名>_<列...>`（唯一）、`idx_<表名>_<列...>`（普通）。阿里原文的 `pk_字段名`/`uk_字段名` 在 PostgreSQL 下要求 schema 内全库唯一，按此适配落地。
6. **迁移**：`<context>_<NNNN>_<desc>.sql` 只追加；改名/扩宽 CHECK 用新迁移；一个文件只属一个上下文。
7. **外键**：跨上下文不建 FK，只做 BIGINT 逻辑引用；同上下文内 FK 可选。
8. **例外**：`schema_migrations`（迁移框架表，类比 `flyway_schema_history`）不改名；历史阶段规格（P2/P3/P4 spec）作为交付记录保留旧名，主蓝图 §10.2 更新为规范名。

---

## 7. 本文件边界

- **不是设计文档**：设计事实、决策、原则、不变量、约束一律从主蓝图读取。
- **不是进度文档**：阶段进度、任务状态、分支/环境状态不属于本文件。
- **不是规范全文**：实现细节以详细设计文档为准。
- **不替代阅读**：本文件只负责指路与权威判定；动手前仍需按 §1–§2 实际查阅并给出依据。
