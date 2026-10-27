# EzTest P4 代码理解与影响分析 v1 — 设计规格

> **状态**：待用户评审（2026-10-28，rev1：影响对象/T1 形态/验收仓库已由用户确认）
> **范围**：实施阶段 P4（`docs/design/implementation-phases.md` §2-P4、§2.9 #14）与代码智能里程碑 CI-M0（`docs/design/code-intelligence.md` §11.1）
> **设计权威**：`docs/design/system-blueprint.md` v2.2（D1–D31；重点 D17–D25、D29、D30）；本规格拟新增 D32/D33 并同步详细设计（见 §13）
> **配套**：P0/P1/P2/P3 规格；`docs/design/code-intelligence.md`；`docs/design/frontend-design.md` §4.3.8/§6；`Agent.md` §6
> **性质**：P4 实现依据（范围/架构/DDL/接口/契约/验收），不含任务拆解；任务拆解由 `writing-plans` 产出。

---

## 1. 目标与非目标

### 1.1 目标

交付「本地 Git 真源 → T0 确定性层 → T1 AI 代码理解 → 跨栈契约 → 影响分析 v1」垂直切片，覆盖后端、只读工具、前端与验收：

1. **T0 通用层（确定性、语言无关）**：只读解析本地仓库固定分支；commit/文件树/文件 blob 哈希/文件级 diff/按需 unified diff/commit 元数据；文件级锚点；大仓分页与预算。
2. **T1 AI 代码理解**：对变更文件（默认 ≤20）用 AI 产出结构化事实——页面/路由/接口/组件/前端调用点/依赖/变更语义；每条事实带证据与置信度。
3. **证据存在性校验**：`path + start/end line + 摘录 sha256` 必须能按 **commit 内容**复核；不通过的事实降为 low 线索，不参与自动影响判定。
4. **缓存与版本化**：文件级分析按 `(repo, commit, path, file_hash, model, prompt_version)` 复用；模型/提示词升级产生新 fact 版本（旧版保留）；force 产生新影响分析版本，不覆盖旧结果。
5. **跨栈 API 契约**：AI 识别前端调用点与后端接口端点，以 `method + 归一化 path` 匹配，落为 `code_edges.rel='route_to_api'`；任一侧变更沿契约传播影响。
6. **影响分析 v1**：以版本绑定的 `code_baseline_commit` 为 base、固定分支 tip（或显式 head）为 head；输出受影响**需求**、受影响**应用模型节点**、`unmapped_changes`，每项带原因/置信度/证据；人工可勾选/取消；无变更/未索引/AI 不可用均有明确状态。
7. **锚点**：需求/版本/应用模型 ↔ 文件/位置的可编辑锚点（P4 可用）；`case`/`issue` 主体类型预留，P5/P6 接入。
8. **前端**：版本详情「代码与变更」Tab 真实现（commit 头、变更文件与 diff、AI 事实与证据、影响面、未映射、人工调整）；代码仓管理页索引状态与「刷新索引」。
9. **降级**：LLM 未配置/不可用/超预算 → 影响分析以 `degraded` 状态返回 T0 文件级结果，UI 明确标注，主流程不阻塞。
10. **只读工具薄壳**：为 P5 Agent 提供 `code.*` 只读工具（复用同一服务）；代码读取审计；不新增 Agent profile。

### 1.2 非目标（P4 明确不做）

- parser/静态语言解析、语法树、符号表、符号级调用链、字段级分析（D19；`code-intelligence.md` §1.2）。
- 用例/问题实体与 `code_ref` UI（P5/P6/P7）；`affected.cases` 仅保留响应结构。
- 批次/执行/暂停恢复/实时播报（P6）；批次时间线与「重跑受影响用例」（P6/P7）。
- 事实人工修正入口、事实质量指标采集（CI-M1，`code-intelligence.md` §12.4）。
- 全仓 AI 分析、全仓索引大屏、全文检索（D24：仅路径/符号/路由/接口查询 + 按需读片段）。
- monorepo 子项目根目录配置/自动识别（D20：P4 只做多目录文件级；子项目映射后移）。
- 线上代码仓（GitHub/GitLab）、分支列表/切换、工作树修改、执行代码（D17/D21/D22）。
- 代码事实质量评测、影响漏报/误报度量（CI-M1）。
- 多实例分析 worker、分析任务跨进程恢复（P8 部署形态；P4 单实例进程内队列）。
- 新增 Go/前端依赖；前端新增重型图库/表单库/状态库。
- 新 Agent profile；P4 不改变 P3 `code.tree`/`code.read` 工具契约。

---

## 2. 已确认约束与决策

| 项 | 决策 | 来源 |
|---|---|---|
| 交付切分 | 单个 spec + 单个 plan；实施按 Wave/并发/依赖/写作用域标注 | 用户 2026-10-28；P1–P3 惯例 |
| 限界上下文 | 单个 `codeintel` 上下文，DDD 四层齐全；跨上下文只经 application 端口；无跨上下文 FK | `Agent.md` §6.2/D26 |
| 影响对象 | v1 输出 **需求 + 应用模型 + unmapped**；`affected.cases=[]` 预留，P5 接入 | 用户 Q1（2026-10-28） |
| T1 执行形态 | **服务化**：应用服务直调 LLM 网关（chat 协议），分析任务落库异步执行（202/轮询/SSE）；提供 `code_*` 只读工具薄壳；**不新增 Agent profile** | 用户 Q2（2026-10-28） |
| 验收仓库 | fixture 仓库用于自动 E2E 与确定性验收；**EzTest 自身仓库真实 commit 对 + 真实 LLM**用于人工验收 | 用户 Q3（2026-10-28） |
| 代码真源 | P2 已配置的本地 Git 仓；只读、固定分支、不切分支、不改工作树；base=版本 `code_baseline_commit` | D17/D21/D22；P3 绑定 |
| 仓库配置复用 | 复用 `resource.code_repositories`（P2 BIGSERIAL，API 中为 number）；P4 只经 resource application 端口写 `last_indexed_at`，不改 P2 表结构 | D28/D29；本规格 |
| 新增主键 | 雪花 ID（BIGINT，应用层生成）；无 SERIAL/默认值；repo_id 为 BIGINT 逻辑引用 | D29 |
| ID 序列化 | codeintel 自有 ID、requirement 主体 ID、app_model 主体 ID 在 codeintel DTO 中为十进制**字符串**；`repoId`/`createdBy`/`updatedBy` 为 number；事件 refs 为字符串 | D29；本规格 |
| 文件级缓存键 | `(repo_id, commit_sha, path, file_hash, model, prompt_version)`；成功结果才缓存 | 本规格（细化 `code-intelligence.md` §5.2） |
| 证据校验基准 | 以 **commit-pinned 内容**校验（不按工作树；工作树 dirty 以 commit 为准） | 本规格（细化 `code-intelligence.md` §5.3、§10.3/D21） |
| 事实版本化 | `code_fact_versions` 按 fact_key 递增版本；旧版 `superseded_at` 保留；`code_entities` 保存当前版本 | `code-intelligence.md` §5.3/§7.4；本规格 |
| 影响分析版本化 | 同输入（version+base+head+model+prompt+fact 版本集）默认复用；`force` 新建 `analysis_version`，不覆盖 | `code-intelligence.md` §7.4；本规格 |
| 跨栈契约 | AI 识别调用点/端点；`method + 归一化 path` 匹配；存 `code_edges.rel='route_to_api'`，不另建契约表 | `code-intelligence.md` §8；本规格 |
| 影响算法 | 确定性候选（diff/facts/锚点/精确匹配）+ 需求关系一跳 + AI 影响推理；优先级 direct > contract > relation > ai；low 默认不选中、可人工选中 | `code-intelligence.md` §7.2；用户 Q1；本规格 |
| 应用模型匹配 | 按 P2 key 约定（`page:`/`route:`/`api:METHOD`/`component:`）归一匹配 + 人工锚点；不新增 AI 映射调用 | `code-intelligence.md` §7.2；P2 appmodel |
| API 前缀 | 仓库级 `/api/code/*`；版本级 `/api/versions/{id}/code/*`（细化蓝图 §11.1 的 `/api/code/impact` 为版本作用域） | 蓝图 §11.1；D32 |
| 权限 | repo 配置/索引 admin；代码读取/事实查询/版本分析/锚点/人工调整 = 全部登录用户；未登录 401 | 蓝图 §10.2；P3 惯例 |
| 异步执行 | `POST` 触发返回 202 + run id；状态可查；单实例进程内 worker（默认并发 1）；启动恢复遗留 running | 用户 Q2；本规格 |
| 实时事件 | 版本触发完成发 `code.changed`、`code.impact_computed`（`version:{id}`、`refs.code`、仅摘要计数）；repo 级索引不广播 | 蓝图 §11.2；D32 |
| 降级 | LLM 不可用/超预算 → analysis `status=done` + `degraded=true`（HTTP 仍 2xx），T0-only；不返回 503 给版本流程 | P4 退出标准；`code-intelligence.md` §2 |
| 测试 | fake LLM E2E + gated 真实 LLM；fixture 双 commit；EzTest 自仓库真实 diff；`test.ps1`/`e2e.ps1`/`acceptance.ps1` | 用户 Q3；P0–P3 惯例 |
| 规模预算 | spec ≈1.0–1.2k 行；plan ≈20–24 个任务、8 个 Wave | 本规格 |

---

## 3. 架构与仓库结构

### 3.1 形态

- 单体 Go 服务 + Next.js。新增 `codeintel` 限界上下文，四层齐全；在 `backend/cmd/eztest/main.go` 组装。
- `codeintel` **不导入** `version` / `resource` / `agent` 的 infrastructure 或 interfaces；跨上下文只使用对方 application 端口（`Agent.md` §6.2）。
- P3 `version/infrastructure/gitread` 与 `code.tree`/`code.read` 工具**保持原样**；codeintel 自带只读 Git 适配器 `infrastructure/gitx`（命令、超时、路径安全、`GIT_OPTIONAL_LOCKS=0` 等规则与 P3 对齐，但不共享可变状态）。
- T1 不进入 Agent 运行时：`analysis_service` 直调 `llm.Client.Chat`（chat_completions，JSON 提示词），证据校验与物化由应用层完成；Agent 工具只是薄壳。
- 异步分析 worker 运行在 API 进程内：DB 记录状态；HTTP 202 返回；前端轮询 + 版本 SSE 事件。单实例假设写入 §15。
- 影响分析不修改 Git 工作树、不切换分支；head 每次从固定分支实时 `rev-parse`，不信任 `code_repositories.last_commit` 缓存。

### 3.2 目录树

```text
backend/
├─ cmd/eztest/main.go                     # 唯一组装根：codeintel 仓储/服务/worker/工具注册
└─ internal/
   ├─ codeintel/                          # P4 新增限界上下文
   │  ├─ domain/
   │  │  ├─ entity.go                     # CodeEntity/Kind/Confidence/Evidence
   │  │  ├─ edge.go                       # CodeEdge/rel
   │  │  ├─ change_set.go                 # CodeChangeSet/ChangedFile
   │  │  ├─ analysis.go                   # CodeAnalysis/Status/Degraded/统计
   │  │  ├─ fact_version.go               # CodeFactVersion
   │  │  ├─ anchor.go                     # CodeAnchor/SubjectKind
   │  │  ├─ evidence.go                   # ExcerptHash/行号/摘录规则
   │  │  ├─ contract.go                   # method+归一化 path 规则
   │  │  ├─ limits.go                     # P4 常量（§4.2）
   │  │  ├─ repository.go                 # 仓储接口
   │  │  ├─ filter.go                     # 分页/过滤参数
   │  │  └─ errors.go                     # 领域错误/reason
   │  ├─ application/
   │  │  ├─ ports.go                      # GitReader/LLM/RepoCatalog/VersionCatalog/…
   │  │  ├─ index_service.go              # T0 索引 run
   │  │  ├─ analysis_service.go           # T1 + change set + 触发/查询
   │  │  ├─ impact_service.go             # 候选合并/排序/人工调整
   │  │  ├─ anchor_service.go             # 锚点 CRUD/校验
   │  │  ├─ prompt.go                     # 提示词原文与 prompt version
   │  │  ├─ parser.go                     # LLM JSON 严格解析
   │  │  ├─ run_worker.go                 # 进程内队列/状态机/恢复
   │  │  ├─ agent_tool_service.go         # code_* 工具原语（薄）
   │  │  └─ dto.go                        # 应用层视图模型
   │  ├─ infrastructure/
   │  │  ├─ gitx/read.go                  # tree/blob/commit/HEAD/dirty/read lines
   │  │  ├─ gitx/diff.go                  # name-status -z / unified diff
   │  │  ├─ gitx/validate.go              # 路径/commit/分支校验
   │  │  ├─ postgres/{index_run,change_set,entity,edge,fact,analysis,candidate,anchor,cache}_repository.go
   │  │  ├─ llm/gateway.go                # llm.Client.Chat 适配 + 配置快照
   │  │  ├─ eventing/publisher.go         # version:{id} 事件
   │  │  ├─ resource/catalog.go           # 读 repo/应用模型（resource application）
   │  │  ├─ resource/index_state.go       # 写 last_indexed_at（resource application）
   │  │  ├─ version/catalog.go            # 读版本绑定/需求/关系（version application）
   │  │  └─ audit/recorder.go             # 代码读取/分析审计
   │  ├─ interfaces/httpapi/
   │  │  ├─ routes.go
   │  │  ├─ handlers_repos.go             # /api/code/repos/*
   │  │  ├─ handlers_changes.go           # diff/file/tree/facts/search
   │  │  ├─ handlers_analyses.go          # /api/versions/{id}/code/*
   │  │  ├─ handlers_anchors.go
   │  │  ├─ dto.go
   │  │  └─ helpers.go
   │  └─ integration/                     # //go:build integration
   │     ├─ migrate_integration_test.go
   │     ├─ index_integration_test.go
   │     ├─ analysis_integration_test.go
   │     ├─ impact_integration_test.go
   │     └─ wiring_integration_test.go
   ├─ agent/infrastructure/tools/codeintel.go   # code_* 工具薄壳（本地窄端口）
   ├─ resource/application/coderepo_service.go  # 扩展：MarkIndexed
   ├─ server/server.go                          # Dependencies.CodeIntel + 路由挂载
   └─ config/config.go                          # 新增 EZTEST_CODEINTEL_* 配置

frontend/src/
├─ app/(app)/versions/[id]/page.tsx       # 只组装；挂载 CodeChangesTab
├─ features/code-intel/                   # P4 新 feature
│  ├─ api.ts types.ts
│  ├─ CodeChangesTab.tsx CommitHeader.tsx ChangedFiles.tsx DiffDrawer.tsx
│  ├─ FactsPanel.tsx EvidenceLink.tsx ImpactPanel.tsx UnmappedChanges.tsx
│  ├─ AnchorDialog.tsx RunStatus.tsx empty.tsx
│  └─ *.test.tsx
├─ features/versions/{VersionTabs.tsx,VersionDetailPage.tsx}  # code slot 替换占位
└─ features/code-repos/{CodeReposPage.tsx,api.ts,types.ts}    # 索引状态/刷新

scripts/
├─ fixture-codeintel-repo.ps1             # 生成 .tmp/p4-e2e-repo（双 commit）
├─ acceptance-codeintel-llm.ps1           # gated 真实 LLM 验收（fixture + 自仓库 diff）
├─ fake-llm.cjs                           # 新增 P4 场景
└─ e2e.ps1                               # 注入 P4 fixture/env
```

### 3.3 请求流

**读路径（同步、只读）**

```text
GET /api/code/repos/{id}/{tree|file|diff|diff/file|facts|search}
  → Handler（登录校验、参数/路径校验）
  → application.Query（或 gitx 只读适配）
  → 返回摘要/片段（限长、分页）；file/search 读取写 audit（action=code.read）
```

**仓库 T0 索引（异步）**

```text
POST /api/code/repos/{id}/index (admin)
  → 校验 repo active、解析固定分支 commit
  → code_index_runs(queued) + worker.enqueue → 202
Worker: running → gitx.tree（include/exclude，≤MaxIndexFiles，分页/截断标记）
      → 写 code_entities(kind=file, source=git, blob_hash)
      → resource.MarkIndexed(repo, commit, now)
      → status=done（stats：files/truncated/duration）
```

**版本影响分析（异步）**

```text
POST /api/versions/{id}/code/analyses {headCommit?, force?}
  → 校验 version 绑定 repo + baseline commit；head=显式或固定分支 tip；base≠head
  → 若同 (version, base, head, model, prompt) 已有 done/degraded 且非 force → 200 复用
  → 若该版本有 queued/running → 409 analysis_in_progress
  → code_analyses(queued, analysis_version=max+1) → 202
Worker:
  1) gitx.diff(base..head) → code_change_sets（同 repo+base+head 复用）+ 事件 code.changed
  2) 选文件（≤MaxChangedFilesAnalyzed；文本/大小过滤）→ 缓存命中或 LLM Chat 分析
  3) 严格解析 JSON → 证据按 commit 校验 → 物化 code_entities/code_edges/code_fact_versions
  4) 影响候选：锚点/精确匹配/契约/需求关系一跳/AI 推理 → code_impact_candidates
  5) unmapped 计算 → code_analyses(unmapped)
  6) status=done（degraded 可为 true）+ 事件 code.impact_computed
```

### 3.4 领域服务

| 服务 | 职责 |
|---|---|
| `IndexService` | repo T0 索引 run：树/文件哈希/commit 元数据；写 `code_index_runs`/`code_entities(file)`；更新 `last_indexed_at` |
| `AnalysisService` | 版本分析 run：change set、T1 文件分析（缓存/版本）、事实物化、状态机、复用/force 语义、事件 |
| `ImpactService` | 由 facts/diff/锚点/需求关系/AI 推理生成候选；去重排序；人工调整；unmapped；degraded |
| `AnchorService` | 锚点创建/删除/查询；主体归属校验；重复检测 |
| `AgentToolService` | `code_*` 工具原语（摘要、限长、审计）；不绕过服务层 |
| `RunWorker` | 单实例队列；状态迁移；超时/失败；启动恢复；事件发布 |

### 3.5 跨上下文端口

| 端口（codeintel application 定义） | 实现（infrastructure） | 提供方 application | 用途 |
|---|---|---|---|
| `GitReader` | `gitx`（本地实现） | — | tree/blob/commit/HEAD/diff/read lines/dirty |
| `LLMClient` / `LLMConfig` | `llm/gateway` | `llm/application`（ConfigService.GetDefault + ClientFactory） | chat 调用、模型名、prompt 版本快照 |
| `RepoCatalog` | `resource/catalog` | `resource/application`（CodeRepo 读接口） | 读 localPath/fixedBranch/globs/status/lastIndexedAt |
| `IndexStateWriter` | `resource/index_state` | `resource/application`（**P4 新增** `MarkIndexed`） | 写 `last_indexed_at` |
| `AppModelCatalog` | `resource/catalog` | `resource/application`（AppModel 查询） | 应用模型节点（page/route/api/component） |
| `VersionCatalog` | `version/catalog` | `version/application`（Version/Requirement/Relation 读） | 版本绑定、需求、关系 |
| `EventPublisher` | `eventing/publisher` | `eventing/application` | `version:{id}` 事件（`code.changed`/`code.impact_computed`） |
| `AuditRecorder` | `audit/recorder` | `audit/application` | 代码读取/分析审计 |
| `IDGenerator` / `Clock` | `platform/id` / `platform/clock` | — | 雪花 ID / 时间 |

- P4 在 `resource/application` 新增 `MarkIndexed(ctx, repoID int64, commit string, at time.Time) error`（或等价窄方法），由 `CodeRepoService` 校验 repo 存在后更新 `last_indexed_at`；不新增表、不改 P2 表结构。
- `version/application` 只新增**只读**查询方法（如需暴露需求/关系列表），不改 P3 现有语义；codeintel 不直接读 version 表。
- 所有适配器在 `cmd/eztest` 注入；codeintel 不感知对方 infrastructure。

### 3.6 组装根与 worker 生命周期

- `cmd/eztest/main.go` 启动顺序：config → db/migrate → 既有上下文 → resource `MarkIndexed` → codeintel 仓储/服务 → worker 启动 → agent 工具注册（**registry 构建前** append `codeintel` 工具）→ server 装配 → 优雅停机时停止 worker。
- worker：有界队列（默认 16）、并发 1（`EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES`）；每次执行带 `analysis_timeout_ms` 超时；panic 捕获并以 `failed` 落库。
- 启动恢复：将 `queued|running` 且 `started_at` 早于阈值（≥10 分钟）的 `code_analyses`/`code_index_runs` 标记 `failed`（reason=`interrupted`），允许用户重触发；单实例假设。

---

## 4. 运行配置

### 4.1 配置

P4 新增以下环境变量（`config.Config` 增加对应字段；启动时校验范围，非法即启动失败）：

| 变量 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `EZTEST_CODEINTEL_MAX_CHANGED_FILES` | `20` | 1–200 | 单次分析最多送入 LLM 的变更文件数；超出按 §6.2 规则截断并计入 stats |
| `EZTEST_CODEINTEL_MAX_FILE_BYTES` | `65536` | 1024–1048576 | 单个文件参与 AI 分析/读取的最大字节；超过视为 unsupported |
| `EZTEST_CODEINTEL_MAX_INDEX_FILES` | `5000` | 1–50000 | 单次 T0 索引落库的文件实体上限；超出 `truncated=true` |
| `EZTEST_CODEINTEL_ANALYSIS_TIMEOUT_MS` | `180000` | 30000–600000 | 单次分析 run 总超时（含所有 LLM 调用） |
| `EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES` | `1` | 1–4 | worker 并发上限（单实例） |
| `EZTEST_CODEINTEL_PROMPT_VERSION` | `p4.1` | 1–32，`[a-z0-9.-]` | 提示词版本；参与缓存键与 fact 版本；变更即失效旧缓存 |
| `EZTEST_CODEINTEL_MAX_EXCERPT_LINES` | `40` | 1–200 | 证据摘录最大行数（同时受 `MaxExcerptLines` 领域上限约束） |
| `EZTEST_CODEINTEL_MAX_DIFF_BYTES` | `262144` | 4096–1048576 | 单文件 unified diff 返回上限；超出 `truncated=true` |

- `.env.example` 追加上述键（含注释），不写真实值；`docs/dev-setup.local.md` 不需要新增本地凭据。
- 模型、重试、超时沿用 P0 `llm` 配置（`GetDefault`）；P4 不新增模型配置项与 Key。
- Docker Compose 不强制新增 volume；部署时由运维把代码仓目录以**只读**方式挂载进 API 容器，`localPath` 指向容器内可见路径（README 说明）。`.env.example` 与文档同步。

### 4.2 领域常量（`codeintel/domain/limits.go`）

| 常量 | 值 | 用途 |
|---|---|---|
| `MaxFactsPerFile` | 200 | 单文件 AI 事实条数上限 |
| `MaxEdgesPerFile` | 200 | 单文件 AI 边上限 |
| `MaxEvidencePerFact` | 8 | 单事实证据条数上限 |
| `MaxExcerptLines` | 200 | 证据摘录行数硬上限（配置值不得超此值） |
| `MaxFactKeyChars` | 512 | fact_key 长度 |
| `MaxReasonChars` | 2000 | 候选原因/未映射原因 |
| `MaxPathChars` | 4096 | 文件路径长度 |
| `MaxUnmappedEntries` | 200 | 单次分析 unmapped 条数（超出截断） |
| `MaxChangedFilesStored` | 2000 | change set 存储的变更文件上限（超出 truncated） |
| `MaxTreeEntriesPerPage` | 200 | tree 接口单页上限 |
| `MaxReadLines` | 400 | file 接口单次行数上限 |
| `MaxRequirementsInPrompt` | 100 | AI 影响推理时需求清单上限 |
| `MaxAnalysisErrors` | 20 | 分析错误条目上限 |
| `MaxFactVersionsSnapshot` | 500 | 分析记录中 fact 版本快照上限 |
| `MaxFileReadBytes` | 64 KiB（与 P3 gitread 对齐） | 按需读取内容上限 |

### 4.3 本地运行与测试命令

```powershell
cd backend; ..\.tools\go\bin\go.exe test ./internal/codeintel/...
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1 ./internal/codeintel/...
cd frontend; pnpm vitest run src/features/code-intel
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/e2e.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance.ps1
$env:EZTEST_RUN_REAL_LLM="1"; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/acceptance-codeintel-llm.ps1
```

### 4.4 迁移与种子

- 新增两个迁移并**追加**到 `backend/internal/db/migrations/order.txt` 末尾（第 15、16 位）：`codeintel_0001_code_facts.sql`、`codeintel_0002_code_impact.sql`；不修改任何已应用迁移。
- 不写业务样例数据；提示词为代码常量 + `EZTEST_CODEINTEL_PROMPT_VERSION`，不落库。
- 不新增 Agent profile seed；`code_*` 工具仅注册到 registry（P3 profile 的 `allowed_tools` 不变，P5 再配置）。

---

## 5. 数据模型与迁移

> P4 新增 9 表，主键均为应用层雪花 ID：`id BIGINT PRIMARY KEY`，**无序列、无默认值**；`repo_id` 为 P2 `code_repositories.id`（BIGSERIAL）的逻辑引用，**不建跨上下文 FK**；`version_id`、`requirement_id`、`app_model_id` 同为逻辑引用。JSONB 正文上限由应用层控制（§4.2）。

### 5.1 `codeintel_0001_code_facts.sql`

```sql
-- P4 ids are Snowflake values generated by platform/id; no sequence/default.
CREATE TABLE IF NOT EXISTS code_change_sets (
  id            BIGINT PRIMARY KEY,
  repo_id       BIGINT NOT NULL,
  base_commit   TEXT NOT NULL CHECK (base_commit ~ '^[0-9a-f]{40}$'),
  head_commit   TEXT NOT NULL CHECK (head_commit ~ '^[0-9a-f]{40}$'),
  stats         JSONB NOT NULL DEFAULT '{}'::jsonb,
  changed_files JSONB NOT NULL DEFAULT '[]'::jsonb,
  truncated     BOOLEAN NOT NULL DEFAULT FALSE,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (repo_id, base_commit, head_commit)
);

CREATE TABLE IF NOT EXISTS code_index_runs (
  id          BIGINT PRIMARY KEY,
  repo_id     BIGINT NOT NULL,
  commit_sha  TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
  source      TEXT NOT NULL DEFAULT 't0' CHECK (source IN ('t0')),
  status      TEXT NOT NULL DEFAULT 'queued'
              CHECK (status IN ('queued','running','done','failed')),
  stats       JSONB NOT NULL DEFAULT '{}'::jsonb,
  errors      JSONB NOT NULL DEFAULT '[]'::jsonb,
  started_at  TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_by  BIGINT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_code_index_runs_repo
  ON code_index_runs(repo_id, created_at DESC);

CREATE TABLE IF NOT EXISTS code_entities (
  id           BIGINT PRIMARY KEY,
  repo_id      BIGINT NOT NULL,
  commit_sha   TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
  kind         TEXT NOT NULL CHECK (kind IN (
                 'file','page','route','api','client_call','component','module','symbol')),
  path         TEXT NOT NULL CHECK (path <> '' AND length(path) <= 4096),
  name         TEXT NOT NULL DEFAULT '',
  start_line   INT CHECK (start_line IS NULL OR start_line >= 1),
  end_line     INT CHECK (end_line IS NULL OR end_line >= 1),
  signature    TEXT NOT NULL DEFAULT '',
  blob_hash    TEXT NOT NULL DEFAULT ''
               CHECK (blob_hash = '' OR blob_hash ~
                 '^(git-sha1:[0-9a-f]{40}|sha256:[0-9a-f]{64})$'),
  source       TEXT NOT NULL CHECK (source IN ('git','ai')),
  confidence   TEXT NOT NULL DEFAULT 'high' CHECK (confidence IN ('high','medium','low')),
  evidence     JSONB NOT NULL DEFAULT '[]'::jsonb,
  meta         JSONB NOT NULL DEFAULT '{}'::jsonb,
  fact_key     TEXT,
  fact_version INT,
  analysis_id  BIGINT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (end_line IS NULL OR start_line IS NULL OR end_line >= start_line),
  CHECK (fact_key IS NULL OR (length(fact_key) BETWEEN 1 AND 512)),
  CHECK ((fact_key IS NULL) = (fact_version IS NULL)),
  CHECK (source = 'ai' OR fact_key IS NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_code_entities_file
  ON code_entities(repo_id, commit_sha, path) WHERE kind = 'file' AND source = 'git';
CREATE UNIQUE INDEX IF NOT EXISTS uq_code_entities_fact
  ON code_entities(repo_id, commit_sha, fact_key) WHERE fact_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_code_entities_kind
  ON code_entities(repo_id, commit_sha, kind);
CREATE INDEX IF NOT EXISTS idx_code_entities_path
  ON code_entities(repo_id, commit_sha, path);

CREATE TABLE IF NOT EXISTS code_edges (
  id            BIGINT PRIMARY KEY,
  repo_id       BIGINT NOT NULL,
  commit_sha    TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
  src_entity_id BIGINT NOT NULL,
  dst_entity_id BIGINT NOT NULL,
  rel           TEXT NOT NULL CHECK (rel IN (
                  'contains','imports','defines','calls','renders','route_to_api')),
  source        TEXT NOT NULL CHECK (source IN ('git','ai')),
  confidence    TEXT NOT NULL DEFAULT 'medium'
                CHECK (confidence IN ('high','medium','low')),
  evidence      JSONB NOT NULL DEFAULT '[]'::jsonb,
  meta          JSONB NOT NULL DEFAULT '{}'::jsonb,
  fact_key      TEXT,
  fact_version  INT,
  analysis_id   BIGINT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((fact_key IS NULL) = (fact_version IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_code_edges_fact
  ON code_edges(repo_id, commit_sha, fact_key, fact_version,
                src_entity_id, rel, dst_entity_id) WHERE fact_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_code_edges_src
  ON code_edges(repo_id, commit_sha, src_entity_id);
CREATE INDEX IF NOT EXISTS idx_code_edges_dst
  ON code_edges(repo_id, commit_sha, dst_entity_id);

CREATE TABLE IF NOT EXISTS code_fact_versions (
  id             BIGINT PRIMARY KEY,
  repo_id        BIGINT NOT NULL,
  commit_sha     TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
  fact_key       TEXT NOT NULL CHECK (length(fact_key) BETWEEN 1 AND 512),
  version        INT NOT NULL CHECK (version >= 1),
  model          TEXT NOT NULL DEFAULT '',
  prompt_version TEXT NOT NULL CHECK (prompt_version <> ''),
  body           JSONB NOT NULL DEFAULT '{}'::jsonb,
  analysis_id    BIGINT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  superseded_at  TIMESTAMPTZ,
  UNIQUE (repo_id, commit_sha, fact_key, version)
);
CREATE INDEX IF NOT EXISTS idx_code_fact_versions_lookup
  ON code_fact_versions(repo_id, commit_sha, fact_key, version DESC);

CREATE TABLE IF NOT EXISTS code_analysis_cache (
  id             BIGINT PRIMARY KEY,
  repo_id        BIGINT NOT NULL,
  commit_sha     TEXT NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
  path           TEXT NOT NULL CHECK (path <> ''),
  file_hash      TEXT NOT NULL CHECK (file_hash ~
                   '^(git-sha1:[0-9a-f]{40}|sha256:[0-9a-f]{64})$'),
  model          TEXT NOT NULL CHECK (model <> ''),
  prompt_version TEXT NOT NULL CHECK (prompt_version <> ''),
  facts          JSONB NOT NULL DEFAULT '[]'::jsonb,
  stats          JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (repo_id, commit_sha, path, file_hash, model, prompt_version)
);
CREATE INDEX IF NOT EXISTS idx_code_analysis_cache_lookup
  ON code_analysis_cache(repo_id, commit_sha, path, last_used_at DESC);
```

### 5.2 `codeintel_0002_code_impact.sql`

```sql
CREATE TABLE IF NOT EXISTS code_analyses (
  id              BIGINT PRIMARY KEY,
  version_id      BIGINT NOT NULL,
  repo_id         BIGINT NOT NULL,
  base_commit     TEXT NOT NULL CHECK (base_commit ~ '^[0-9a-f]{40}$'),
  head_commit     TEXT NOT NULL CHECK (head_commit ~ '^[0-9a-f]{40}$'),
  change_set_id   BIGINT,
  status          TEXT NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued','running','done','failed')),
  degraded        BOOLEAN NOT NULL DEFAULT FALSE,
  model           TEXT NOT NULL DEFAULT '',
  prompt_version  TEXT NOT NULL CHECK (prompt_version <> ''),
  analysis_version INT NOT NULL DEFAULT 1 CHECK (analysis_version >= 1),
  fact_versions   JSONB NOT NULL DEFAULT '[]'::jsonb,
  stats           JSONB NOT NULL DEFAULT '{}'::jsonb,
  errors          JSONB NOT NULL DEFAULT '[]'::jsonb,
  unmapped        JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_by      BIGINT NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at      TIMESTAMPTZ,
  finished_at     TIMESTAMPTZ,
  UNIQUE (version_id, base_commit, head_commit, analysis_version)
);
CREATE INDEX IF NOT EXISTS idx_code_analyses_latest
  ON code_analyses(version_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_code_analyses_status
  ON code_analyses(status, created_at);

CREATE TABLE IF NOT EXISTS code_impact_candidates (
  id                   BIGINT PRIMARY KEY,
  analysis_id          BIGINT NOT NULL,
  subject_kind         TEXT NOT NULL CHECK (subject_kind IN ('requirement','app_model')),
  subject_id           BIGINT NOT NULL,
  reason               TEXT NOT NULL CHECK (reason <> '' AND length(reason) <= 2000),
  confidence           TEXT NOT NULL CHECK (confidence IN ('high','medium','low')),
  source               TEXT NOT NULL CHECK (source IN (
                         'direct','contract','relation','ai')),
  evidence             JSONB NOT NULL DEFAULT '[]'::jsonb,
  selected             BOOLEAN NOT NULL DEFAULT TRUE,
  selection_updated_by BIGINT,
  selection_updated_at TIMESTAMPTZ,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (analysis_id, subject_kind, subject_id)
);
CREATE INDEX IF NOT EXISTS idx_code_impact_candidates_analysis
  ON code_impact_candidates(analysis_id, subject_kind);

CREATE TABLE IF NOT EXISTS code_anchors (
  id              BIGINT PRIMARY KEY,
  version_id      BIGINT NOT NULL,
  repo_id         BIGINT NOT NULL,
  subject_kind    TEXT NOT NULL CHECK (subject_kind IN (
                    'version','requirement','app_model')),
  subject_id      BIGINT NOT NULL,
  file_path       TEXT NOT NULL CHECK (file_path <> '' AND length(file_path) <= 4096),
  start_line      INT CHECK (start_line IS NULL OR start_line >= 1),
  end_line        INT CHECK (end_line IS NULL OR end_line >= 1),
  entity_id       BIGINT,
  commit_sha      TEXT CHECK (commit_sha IS NULL OR commit_sha ~ '^[0-9a-f]{40}$'),
  confidence      TEXT NOT NULL DEFAULT 'high'
                  CHECK (confidence IN ('high','medium','low')),
  created_by_kind TEXT NOT NULL DEFAULT 'user' CHECK (created_by_kind IN ('user','ai')),
  created_by      BIGINT NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (end_line IS NULL OR start_line IS NULL OR end_line >= start_line)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_code_anchors_point
  ON code_anchors(version_id, subject_kind, subject_id, file_path,
                  COALESCE(start_line, 0), COALESCE(end_line, 0));
CREATE INDEX IF NOT EXISTS idx_code_anchors_subject
  ON code_anchors(version_id, subject_kind, subject_id);
CREATE INDEX IF NOT EXISTS idx_code_anchors_path
  ON code_anchors(repo_id, file_path);
```

### 5.3 迁移规则

- 两个迁移文件只追加到 `order.txt` 末尾（版本=位置）；已应用迁移与 checksum 不改；验收脚本的迁移计数断言同步更新（14 → 16）。
- 一个迁移文件只属于 `codeintel` 上下文；`repo_id`/`version_id`/`subject_id` 均为逻辑引用，不建跨上下文 FK（`Agent.md` §6.2/§6.3）。
- 表内不存二进制/base64；AI facts/证据摘录仅存路径/行号/哈希与结构化摘要（`Agent.md` §6.8/D30）。
- 删除策略：分析/缓存/事实随仓库或版本保留策略后续 GC；P4 不提供删除代码仓索引的级联动作（除 P2 repo 删除外）。

---

## 6. 核心规则

### 6.1 T0 通用 Git 层

**只读解析**：所有 Git 访问使用 argv（无 shell）、`GIT_OPTIONAL_LOCKS=0`、工作目录为仓库根、单命令超时 10s；`localPath` 与 `fixedBranch` 来自 P2 仓库配置；每次请求前校验路径是 work tree、分支可解析（`rev-parse --is-inside-work-tree`、`rev-parse --verify <branch>^{commit}`）。

**commit 与 head**：

- `base_commit` 固定为版本绑定的 `code_baseline_commit`（40 hex）；P4 不允许修改已绑定基线。
- `head_commit` 默认实时解析固定分支 tip；请求可显式传 `headCommit`（40 hex），但必须存在于仓库且满足 `git merge-base --is-ancestor <base> <head>` 成功；否则 422 `invalid_head`。
- **不信任** `code_repositories.last_commit`（仅 P2 校验时刷新的陈旧缓存）。
- `base == head` 合法：分析完成但 `changedFiles=0`，UI 显示「无代码变更」，不调用 LLM。

**文件树**：

- `git ls-tree -r -t -l -z <commit>`；输出 `{path, type(blob/tree), size, gitObjectId(mode 100644/100755 的 blob)、mode}`。
- glob 过滤与 P3 gitread 语义一致：include 为空表示全仓；exclude 优先；对 slash 归一化路径做 shell-style glob；默认排除来自 P2 配置。
- 结果按 path 升序；单页 ≤ `MaxTreeEntriesPerPage`；cursor 为上一页最后 path（不透明编码）。
- symlink（mode 120000）、submodule（160000）标记 `unsupported`，不进入 AI 分析。

**diff（文件级）**：

- 命令（分别执行并按 path 合并）：`git diff --raw -z --no-ext-diff --find-renames=50% <base>..<head>` 与 `git diff --numstat -z --no-ext-diff --find-renames=50% <base>..<head>`；`-z` 防文件名注入。
- 状态：`A`(added)/`M`(modified)/`D`(deleted)/`R<score>`(renamed)/`C<score>`(copied)/`T`(type changed)。
- `ChangedFile`：`{path, oldPath?, status, additions, deletions, binary, headBlobHash?}`；binary 由 numstat `-` 判定；headBlobHash 为 head 侧 `git-sha1:<40hex>`（删除文件为空）。
- 文件数上限 `MaxChangedFilesStored`（2000）；超出 `truncated=true`，按 path 排序截断。
- 同一 `(repo, base, head)` 复用 `code_change_sets` 行；change set 是确定性结果，不因模型/提示词变化而重算。

**按需 unified diff**：

- `git diff --no-ext-diff --find-renames --unified=3 <base>..<head> -- <path>`；仅允许 change set 中的 path；返回 JSON `{path, status, binary, truncated, patch}`；`patch` ≤ `MaxDiffBytes`。
- 二进制/超大/删除文件：`patch` 为空，`binary/truncated` 标注；不阻断 UI。

**按需读取**：

- `git show <commit>:<path>`；严格 UTF-8；按 1-based 行切片；单次 ≤ `MaxReadLines` 行且 ≤ 64 KiB；返回 `{path, commit, startLine, endLine, totalLines, truncated, content}`。
- 路径校验：slash 归一化、拒绝绝对路径/`..`/反斜杠逃逸、解析后必须仍在仓库根内；仅允许文件（非 symlink/submodule）。
- 读取/搜索写审计（§6.10）。

**T0 索引 run**：

- `POST /api/code/repos/{id}/index`：解析固定分支 tip → 创建 `code_index_runs(queued)` → worker 执行。
- 执行：tree 过滤 → 为每个 blob 写 `code_entities(kind=file, source=git, confidence=high, blob_hash=git-sha1:...)`；上限 `EZTEST_CODEINTEL_MAX_INDEX_FILES`（默认 5000），超出 `stats.truncated=true`。
- 完成后经 resource application 端口写 `last_indexed_at`（提交时间用 run.finished_at），不直接 UPDATE P2 表。
- 同一 repo 已有 queued/running index run → 409 `index_in_progress`。

### 6.2 T1 AI 代码理解

**触发**：版本影响分析 run（§6.6）内执行；不做全仓分析。仅分析 change set 中可读取的文本文件：

- 排除：deleted、binary、symlink/submodule、size > `EZTEST_CODEINTEL_MAX_FILE_BYTES`、非 UTF-8。
- 排序（确定性）：`有锚点(1/0) desc → path asc`；取前 `EZTEST_CODEINTEL_MAX_CHANGED_FILES`（默认 20）；其余计入 `stats.skipped`（reason=`not_analyzed`）。
- 每个文件一次 LLM 调用（不做跨文件批量拼接，避免上下文失真）；文件内容为 head commit 的完整文本（≤ 配置上限）。

**LLM 配置与调用**：

- 复用 `llm` 上下文默认配置（`GetDefault`）：model、超时、重试沿用；调用 `llm.Client.Chat`（chat_completions 语义）。
- 未配置/配置缺失/协议不可用 → 该 run 直接 `degraded`（不重试到超时）。
- 提示词 = 系统约束 + 单文件用户消息（`§8.4` 原文）；`EZTEST_CODEINTEL_PROMPT_VERSION` 参与缓存键与事实版本。
- 单文件内容 ≤ 64 KiB；提示词不得包含其他仓库/其他版本内容；不记录代码正文到日志/事件/审计。
- 输出解析（`application/parser.go`）：容忍 markdown code fence；截取首个平衡 JSON 对象；严格校验必填字段与类型；未知字段忽略；解析失败记 `parse_error`，该文件跳过；不做第二次修复调用（P4）。

**AI 输出结构（严格）**：

```json
{
  "changeSummary": "本文件改了什么、影响哪个功能面（≤500 字）",
  "facts": [
    {
      "kind": "route|page|api|client_call|component|module|symbol",
      "name": "GET /orders | POST /api/refund | OrderList | ...",
      "path": "相对仓库根的 slash 路径",
      "startLine": 1,
      "endLine": 20,
      "signature": "可选签名/关键行（≤512 字）",
      "confidence": "high|medium|low",
      "summary": "事实说明（≤500 字）",
      "evidence": [
        {"file": "相对路径", "startLine": 1, "endLine": 5}
      ]
    }
  ],
  "edges": [
    {
      "from": {"kind": "component", "name": "OrderList"},
      "to": {"kind": "route", "name": "/orders"},
      "rel": "contains|imports|defines|calls|renders|route_to_api",
      "confidence": "high|medium|low",
      "evidence": [{"file": "相对路径", "startLine": 1, "endLine": 5}]
    }
  ]
}
```

**结构约束**：

- `facts` ≤ `MaxFactsPerFile`，`edges` ≤ `MaxEdgesPerFile`，`evidence` ≤ `MaxEvidencePerFact`；超出按顺序截断并计入 `stats.truncatedEntries`。
- `kind` 仅上述枚举；`confidence` 仅 high/medium/low；行号必须为正整数且 `startLine ≤ endLine`。
- 事实身份（fact_key）规则：
  - `route`: `route:<name>`（name 为 `/path`；缺前导 `/` 自动补）
  - `page`: `page:<name>`
  - `api`: `api:<METHOD> <normalizedPath>`（name 必须含方法，如 `POST /api/refund`）
  - `client_call`: `client:<METHOD> <normalizedPath>`
  - `component`: `component:<path>#<name>`
  - `module` / `symbol`: `symbol:<path>#<name>`
  - 无法归一化或 name 为空 → 该事实丢弃并计入 `stats.invalidFacts`。
- AI 不得"编造"：提示词要求未知时返回空数组；验收检查无证据事实不以 high/medium 出现。
- **边物化**：AI 输出的 edges 先按 `from/to` 的 `kind+name` 解析为事实身份（fact_key），两端均能解析到当前实体才写入 `code_edges`；`rel` 仅允许枚举值，证据/置信度按 §6.3 校验；任一端无法解析 → 丢弃并计入 `stats.unresolvedEdges`。`route_to_api` 契约边由 §6.5 统一匹配生成（不直接信任 AI 直出的同名边），避免自相矛盾。

**事实置信度**：

- AI 返回 high/medium/low 作为初始值；
- 系统强制降级为 low：证据缺失、证据校验失败、路径/行号不合法；
- `route`/`api`/`client_call`/`page`/`component` 无有效证据时仅作为 low 线索（`meta.clue=true`），不参与自动影响；
- low 事实保留展示（灰显），可被人工确认，但不产生自动候选。

### 6.3 证据存在性校验

**证据格式**：`{file, startLine, endLine}`；服务端计算 `excerptHash` 后存为 `{file, startLine, endLine, excerptHash}`。

**摘录与哈希规范**：

1. 读取 `git show <commit>:<file>` 原始字节（仅文件，≤ 配置单文件上限；二进制/非 UTF-8 直接判失败）。
2. 按 `\n` 切分为行，丢弃每行末尾的 `\r`（CRLF→LF 归一）；行号 1-based。
3. 取 `startLine..endLine` 闭区间行，以 `\n` 连接（不含末尾换行），编码 UTF-8。
4. `excerptHash = "sha256:" + hex(sha256(bytes))`；服务端重新计算并逐字节比较。

**校验步骤（全部通过才算有效）**：

1. `file` 在 head commit 的 tree 中存在且为普通文件（非 symlink/submodule/删除）。
2. `1 ≤ startLine ≤ endLine ≤ 文件总行数`，且 `endLine-startLine+1 ≤ EZTEST_CODEINTEL_MAX_EXCERPT_LINES`。
3. 服务端按上述规则计算摘录哈希并落库；后续任何时刻可用 commit 内容复算校验。**不信任模型提供的哈希**（模型只提供行号区间）。
4. 文件大小 ≤ `EZTEST_CODEINTEL_MAX_FILE_BYTES`（否则无法可靠校验 → 失败）。

**失败处理**：

- 无效证据不进入 `code_entities.evidence`（避免展示假证据）；事实降为 low 线索，记 `meta.invalidEvidence=<count>`；
- `stats.evidenceChecks/evidenceFailures` 计数；错误详情只含 path/行号/失败原因，不含代码正文；
- 事实校验基准**始终是 commit 内容**，与工作树是否 dirty 无关（D21；修正 `code-intelligence.md` §5.3 “工作树读取”的表述）。

### 6.4 缓存与版本化

**文件级缓存**：

- 键：`(repo_id, commit_sha, path, file_hash, model, prompt_version)`；`file_hash` 为 head 侧 Git blob id（`git-sha1:<40hex>`）或内容 sha256（`sha256:<hex>`，非 Git 来源时）。
- 仅缓存成功解析且完成证据校验的文件分析结果（`facts`/`stats`）；失败/超时不缓存。
- 命中：更新 `last_used_at`，直接复用 facts，不调用 LLM；`stats.cacheHits++`。
- `force=true`：跳过缓存重新分析；否则优先缓存。
- 同一 commit 内容不可变，缓存无需 TTL；模型/提示词变化自然 miss。

**事实版本化**：

- 对每个 fact_key：读取当前版本（`code_fact_versions` 最大 version）。
- 计算规范化 body（事实 + 有效证据 + summary；键排序、无浮点）；与当前版本 body 语义相等 → 复用当前版本，不新增；
- 不同 → `version = current+1`，插入新版本；旧版本 `superseded_at=now()`；`code_entities` 只保留当前版本行（`fact_version` 指向最新）。
- 同一 run 内多个文件产生同一 fact_key：按 path 升序合并（取更高置信度、合并有效证据、summary 取首个非空），合并后只产生一个事实/版本；计入 `stats.duplicateFacts`。
- 旧版本仅用于历史追溯/对比（P4 不提供 UI 对比；保留数据）。
- `force` 只保证产生新的 **analysis 版本**；fact 版本仍按 body 是否变化判定：body 完全相同则不新增（避免无意义膨胀），模型/提示词变化或内容差异才 `version+1`。验收通过「prompt 版本变化」或「变更文件内容差异」触发新 fact 版本。

**影响分析版本化与复用**：

- 同 `(version_id, base_commit, head_commit, model, prompt_version, force=false)`：
  - 已有 `status=done`（含 degraded=true）→ 直接返回既有记录（200），不创建新记录；
  - 已有 `status=failed` → 允许重新创建（`analysis_version = max+1`）。
- `force=true`：`analysis_version = max+1`，新记录；旧记录与旧事实版本一律不覆盖。
- 相同输入（含 fact 版本快照）重跑：候选集合、顺序、默认勾选完全一致（INV-4）；验收以两次 GET 的规范化 JSON 对比。
- `analysis_version` 单调递增；唯一键 `(version_id, base_commit, head_commit, analysis_version)`。

### 6.5 跨栈 API 契约

**识别来源**：AI 事实 `client_call`（前端调用点）与 `api`（后端接口端点），均需 high/medium 置信度（low 不参与）。

**归一化**：

1. method 转大写；
2. path：若为绝对 URL，取其 path；去 query/fragment；合并重复 `/`；去末尾 `/`（根除外）；
3. 动态段归一为 `{}`：匹配 `{...}`、`:name`、`<name>`、纯数字、UUID 的段；
4. 保留大小写；不做前缀/网关推断（P4 限制，记入 §15）。

**匹配与存储**：

- 对 head commit 上所有有效 `client_call` × `api` 事实，若 `(method, normalizedPath)` 完全相同则建立契约：
  - `code_edges(src=client_entity, dst=api_entity, rel='route_to_api', source='ai', confidence=min(两侧), evidence=合并有效证据, meta={method, normalizedPath, match:"exact"})`；
  - 单侧事实最多匹配 20 条边；超出计入 `stats.contractTruncated`。
- 契约不另建表；查询走 `code_edges`。
- 端口/前缀差异、路径参数命名差异导致的不匹配 → 不建边，计入 `stats.unmatchedContracts`；若变更文件同时含未匹配的调用点/端点事实，则追加一条 `unmapped_changes`（reason=`unmatched_contract`）。

**传播**：

- 变更 `client_call` → 其 `route_to_api` 对端 `api` → `api:<METHOD> <path>` 应用模型；
- 变更 `api` → 其 `route_to_api` 对端 `client_call` → 对端文件/组件；应用模型仍以 `api` key 命中为主；
- 契约传播只做一跳（CI-M0）。

### 6.6 影响分析 v1

**输入**：

1. `version.code_repository_id` + `version.code_baseline_commit`（base）；`head_commit`（§6.1）。
2. T0 change set（§6.1）。
3. head commit 上已物化的 AI 事实/契约（本次分析产出 + 既有缓存/实体）。
4. 版本需求清单与需求关系（P3）；版本绑定 `system_id` 的应用模型节点（P2）。
5. 锚点（§6.7）。

**流水线**：

1. **T0**：生成/复用 change set；发布 `code.changed`。
2. **T1**：按 §6.2 分析变更文件；缓存命中优先；物化事实/契约。
3. **确定性候选**：
   - **直接锚点**：`code_anchors` 中 `file_path` 属于 change set 的锚点 → 对应 subject（requirement/app_model；version 锚点仅用于展示，不产生影响候选）候选，`source=direct`，`confidence=anchor.confidence`，原因 `changed file <path>`，证据 `[{"file": <path>}]`（锚点来源证据只有文件，无行区间/哈希）。
   - **应用模型精确匹配**：对变更文件产出的有效事实（high/medium）按 §6.5/§6.6 归一 key，与版本绑定系统的应用模型节点 key 完全一致 → 候选，`source=direct`，`confidence=fact.confidence`，原因 `changed <kind> <name>`，证据 = 事实证据；`api` 事实同时匹配 `api:<METHOD> <path>`，`route/page` 匹配同名节点。
   - **契约传播**：变更的 `client_call`/`api` 事实沿 `route_to_api` 一跳 → 对端 `api` 事实对应的应用模型 `api:` 节点；或对端 `client_call` 所在的组件/页面事实对应的应用模型节点 → 候选，`source=contract`，原因 `contract <METHOD> <path> changed`，证据合并。
   - **需求锚点**：requirement 锚点命中变更文件 → 需求候选（与直接锚点同源）。
   - **需求关系一跳**：对已命中的需求，查 `requirement_relations`（任意方向、任意类型）关联需求 → 候选，`source=relation`，`confidence=min(medium, 触发项置信度)`，原因 `related to R<n> via <relation_type>`；不递归；排除已直接命中项。
   - **AI 影响推理**（仅当事实可用且 LLM 可用）：一次调用输入 = 变更摘要（文件、facts 摘要、changeSummary）+ 版本需求清单（≤100，含 id/title/description 摘要）→ 输出受影响需求；`source=ai`；证据按 §6.3 校验；无有效证据或置信度 low → 候选仍记录但 `confidence=low`；未知需求 id 丢弃并计入 `stats.invalidCandidates`。
4. **合并去重**：按 `(subject_kind, subject_id)` 合并；保留最高优先级 source（direct=0 < contract=1 < relation=2 < ai=3）与最高置信度；原因拼接 ≤2000 字符；证据合并去重 ≤5 条。
5. **排序**（确定性）：`source 优先级 asc → confidence(high=0,medium=1,low=2) → subject_kind(requirement=0,app_model=1) → subject_id asc`。
6. **默认勾选**：`selected = (confidence != 'low')`；人工可改。
7. **unmapped_changes**（每个变更文件至多一条，按 path 升序，≤200）：
   - 文件被任何有效事实/锚点覆盖 → 不进入 unmapped；
   - `deleted` → reason `deleted_file`，suggestion「人工确认删除影响」；
   - 二进制/超大/非 UTF-8/未分析（超出文件数上限）→ `unsupported_content`；
   - 无事实且无锚点 → `no_fact_or_anchor`；
   - LLM 不可用且无既有事实 → `ai_unavailable`；
   - 未匹配契约 → `unmatched_contract`（附加条目，不计入文件去重）；
   - 每条 `{path, status, reason, suggestion}`；截断时 `stats.unmappedTruncated=true`。
8. **落库**：`code_analyses`（状态/统计/unmapped/fact_versions 快照/change_summary）+ `code_impact_candidates`；一个事务内写入候选；发布 `code.impact_computed`。

**状态与降级**：

- LLM 未配置/全部调用失败/解析失败比例 100%：`status=done`、`degraded=true`，候选退化为「直接锚点 + 应用模型精确匹配（若有既有事实）+ unmapped」；
- 部分文件失败/缓存命中部分：`done`、`degraded=true`、`errors` 记录文件级原因；
- Git/仓库/base/head 失败：`failed`（不产生候选），错误 reason 见 §7.1；
- 人工调整：`PATCH` 候选 `selected`，写 `selection_updated_by/at`；任何登录用户；不改变 analysis 状态。

### 6.7 锚点

- P4 主体类型：`version` / `requirement` / `app_model`；`case` / `issue` 预留，创建返回 422 `unsupported_subject_kind`。P4 UI 只提供需求锚点入口；app_model/version 锚点通过 API 创建（P5 再补 UI）。
- 归属校验：requirement 必须属于该版本；app_model 必须存在且属于版本绑定的 system（版本未绑定 system 时拒绝 app_model 锚点）；version 主体必须等于路径版本。
- 文件校验：`file_path` slash 归一化、根内校验；在固定分支 tip 上存在且为普通文件；行号可空，提供时 `1 ≤ start ≤ end`。
- 唯一性：同版本+主体+文件+行区间重复 → 409 `anchor_duplicate`。
- 删除：按 id；必须属于路径版本；不存在 404。
- `created_by_kind` P4 固定 `user`（AI 锚点后续）。
- 锚点是影响分析「直接候选」的来源；P4 不自动生成锚点。

### 6.8 异步 run 生命周期

**状态机**：

```text
code_index_runs:   queued → running → done | failed
code_analyses:     queued → running → done（degraded=true 可为终态）| failed
```

- 只允许上述迁移；无取消（P4）；超时/panic → `failed`，reason `analysis_timeout`/`internal`。
- worker 队列有界（16）；满 → 429 `analysis_queue_full`（retryable）。
- 同一版本已有 queued/running 分析 → 409 `analysis_in_progress`。
- 同一 repo 已有 queued/running 索引 → 409 `index_in_progress`。
- 启动恢复：`queued` 直接置 `failed(interrupted)`；`running` 且 `started_at` 超过 10 分钟置 `failed(interrupted)`；允许重新触发。
- worker 停止：进程优雅停机时停止接收新任务，等待当前任务到超时；未完成任务按恢复规则处理。

### 6.9 事件

- 通道：`version:{id}`（复用 P3 SSE）；`version_id` 必填。
- `code.changed`：change set 就绪后发布，`{analysisId, baseCommit, headCommit, stats:{changedFiles, additions, deletions, truncated}}`；`refs.code` = 变更 path（≤20，按 path 排序）。
- `code.impact_computed`：分析完成/降级后发布，`{analysisId, analysisVersion, status, degraded, counts:{requirements, appModels, unmapped, candidates}}`；`refs.code` = 证据 path 去重（≤20）。
- 事件只放 id/计数/路径摘要：`summary ≤500 rune`、`detail ≤64 KiB`（P0 上限）；不放 facts、diff、代码正文。
- 发布失败只记日志，不回滚分析（DB 权威）；repo 级索引不广播（前端轮询 `index-runs/latest`）。
- 前端 `useVersionStream` 过滤 `code.*` 事件刷新面板；未知事件忽略。

### 6.10 权限与审计

| 动作 | 权限 |
|---|---|
| 读取 code 仓库/树/文件/diff/facts/search | 任意登录用户 |
| 触发仓库索引 `POST /api/code/repos/{id}/index` | admin |
| 维护 repo 配置 | admin（P2 `/api/resources/code-repos`） |
| 触发/读取版本影响分析、人工调整候选 | 任意登录用户（与 P3 版本域一致） |
| 锚点增删查 | 任意登录用户 |
| 未登录 | 401 |

- 审计（`audit` 上下文）：`code.read`（file/search 读取，记录 repo/path/commit）、`code.analyze`（分析触发/完成，记录 version/base/head/status）；不记录代码正文、提示词、模型响应原文。
- 审计失败不阻塞主流程（与 P0 一致）。
- 日志：不打印代码内容、不打印 LLM 请求/响应正文；错误信息只含 path/行号/reason。

### 6.11 并发与一致性

- 分析物化（entities/edges/fact_versions/analysis 状态）在**单事务**内完成；候选写入另一事务（保证 analysis 先可见）。
- 缓存 upsert 使用 `ON CONFLICT ... DO UPDATE SET last_used_at=now()`。
- 单实例 worker 串行执行任务；`EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES>1` 时多 goroutine 并行，仍受每版本单 active 限制。
- 事实/候选唯一约束兜底并发重复；冲突时以 DB 现有记录为准并记录 `stats.conflicts`。
- 人工调整使用最后写入胜（记录 updated_by/at）；不引入 revision。

### 6.12 核心不变量

- **INV-1**：low 置信度事实不参与自动影响判定（不产生 direct/contract/relation 候选，AI 候选证据不足则为 low）。
- **INV-2**：每条自动候选（direct/contract/relation）必须能追溯到变更文件或有效事实证据；AI 候选证据必须通过 §6.3 校验。
- **INV-3**：`base = version.code_baseline_commit`；`head` 为固定分支 tip 或 base 的后代；不切分支、不改工作树。
- **INV-4**：相同 `(version, base, head, model, prompt, fact 版本集)` 重跑，候选集合/顺序/默认勾选完全一致。
- **INV-5**：`force` 或模型/提示词变化只新建版本，不覆盖旧 analysis/fact 版本；旧 fact 版本保留 `superseded_at`。
- **INV-6**：`analysis_version` 对 `(version, base, head)` 单调递增且唯一。
- **INV-7**：新增候选默认 `selected = confidence != low`；人工选择持久化且重跑复用同一记录时保持。
- **INV-8**：每个变更文件要么被事实/锚点覆盖，要么出现在 `unmapped_changes`（截断时 stats 标注），不静默丢弃。
- **INV-9**：codeintel 自有 ID、requirement/app_model 主体 ID 在 DTO/事件中为十进制字符串；`repoId`/`createdBy` 为 number；禁止 `Number()` 精度丢失。
- **INV-10**：`degraded` 分析仍返回 T0 候选与 unmapped，HTTP 2xx，不阻塞版本流程。
- **INV-11**：证据校验基于 commit 内容；同一 commit 重跑校验结果一致。
- **INV-12**：缓存键包含 model+prompt_version；任一变化必须 miss 并生成新 fact 版本（若事实变化）。
- **INV-13**：事件/日志/审计不含代码正文与提示词原文；所有输入输出受大小上限约束。
- **INV-14**：codeintel 不直接写 P2 表；`last_indexed_at` 只经 resource application 端口更新。
- **INV-15**：P4 锚点仅 version/requirement/app_model；case/issue 明确拒绝。

---

## 7. API

### 7.1 通用约定

- 仓库级前缀 `/api/code`；版本级前缀 `/api/versions/{id}/code`；成功 `{"data":...}`；分页 `{data,next_cursor,has_more}`；异步触发 202；复用 200；删除 204；错误复用 `platform/errors` 信封。
- **ID 序列化**：
  - codeintel 自有 ID（analysis/indexRun/changeSet/entity/anchor/candidate/fact 版本）一律十进制字符串；
  - `repoId`/`createdBy`/`selectionUpdatedBy` 为 number（P2/身份域既有语义）；
  - 主体 ID `subjectId`：requirement 为字符串（D29），app_model 为字符串（P4 DTO 统一转十进制字符串，前端按 string 处理）；
  - URL 路径中 codeintel ID 为十进制字符串；`repoId` 为十进制数字。
- 请求体 ≤1 MiB（P0 校验）；写操作可带 `Idempotency-Key`；未知字段拒绝。
- 权限按 §6.10；未登录 401；管理员动作对普通用户 403。
- `details.reason` 细分：

| reason | HTTP | 场景 |
|---|---|---|
| `code_repository_not_bound` | 422 | 版本未绑定代码仓 |
| `code_repository_not_found` / `code_repository_disabled` | 422 | P2 仓库不存在/禁用 |
| `baseline_commit_missing` | 422 | 版本无 code_baseline_commit |
| `invalid_head` | 422 | head 非 40hex、不可达或非 base 后代 |
| `commit_not_found` | 422 | 指定 commit 不存在 |
| `git_unavailable` | 503 `unavailable` | Git 命令超时/仓库不可访问（retryable） |
| `path_not_found` / `path_not_file` / `path_escape` | 422 | 路径不存在/非普通文件/越界 |
| `not_changed` | 422 | 请求 diff 的 path 不在 change set |
| `unsupported_content` | 422 | 二进制/非 UTF-8/超大小上限 |
| `index_in_progress` | 409 | 同仓库已有索引 run |
| `index_run_not_found` | 404 | 索引 run 不存在 |
| `analysis_in_progress` | 409 | 同版本已有 queued/running 分析 |
| `analysis_queue_full` | 429 `rate_limited` | worker 队列满（retryable） |
| `analysis_not_found` | 404 | 分析不存在 |
| `analysis_not_done` | 409 | 未完成时调整候选 |
| `candidate_not_found` | 404 | 候选不存在 |
| `anchor_not_found` | 404 | 锚点不存在 |
| `anchor_duplicate` | 409 | 同主体/文件/行区间重复 |
| `unsupported_subject_kind` | 422 | P4 不接受 case/issue 锚点 |
| `subject_not_in_version` | 422 | requirement/version 主体不属于该版本 |
| `app_model_not_found` | 422 | 应用模型节点不存在/不属于绑定系统 |

- 错误信息不回显代码正文、不返回内部路径；`field` 标出参数名。

### 7.2 仓库级 API

| 方法 | 路径 | 说明 | 权限 |
|---|---|---|---|
| GET | `/api/code/repos` | 仓库列表 + 最新索引状态（联表投影） | 登录 |
| POST | `/api/code/repos/{repoId}/index` | 触发 T0 索引（解析固定分支 tip） | admin |
| GET | `/api/code/repos/{repoId}/index-runs/latest` | 最新索引 run | 登录 |
| GET | `/api/code/repos/{repoId}/index-runs/{runId}` | 索引 run 详情 | 登录 |
| GET | `/api/code/repos/{repoId}/tree?commit=&path=&cursor=&limit=` | 文件树（live Git，分页） | 登录 |
| GET | `/api/code/repos/{repoId}/file?commit=&path=&startLine=&endLine=` | 文件行窗口（audit） | 登录 |
| GET | `/api/code/repos/{repoId}/diff?base=&head=&cursor=&limit=` | 文件级变更列表 | 登录 |
| GET | `/api/code/repos/{repoId}/diff/file?base=&head=&path=` | 单文件 unified diff | 登录 |
| GET | `/api/code/repos/{repoId}/facts?commit=&kind=&path=&cursor=&limit=` | 代码事实查询（含 AI 事实） | 登录 |
| GET | `/api/code/repos/{repoId}/search?commit=&q=&kind=&cursor=&limit=` | 路径/名称子串检索（D24，非全文） | 登录 |
| GET | `/api/code/repos/{repoId}/commits/{sha}` | commit 元数据 | 登录 |

`RepoSummaryDTO`：

```json
{
  "id": 3, "key": "web-main", "name": "Web 主仓", "status": "active",
  "fixedBranch": "main", "lastCommit": "abc...", "lastIndexedAt": "2026-10-28T01:02:03Z",
  "index": {
    "runId": "1844...", "commit": "abc...", "status": "done",
    "startedAt": "2026-10-28T01:00:00Z", "finishedAt": "2026-10-28T01:02:03Z",
    "stats": {"files": 1200, "truncated": false}
  }
}
```

`ChangeListDTO`（`/diff` 与版本 `/changes` 共用）：

```json
{
  "changeSetId": "1844...", "baseCommit": "abc...", "headCommit": "def...",
  "stats": {"files": 12, "additions": 340, "deletions": 80, "truncated": false},
  "files": [
    {"path": "src/api/refund.ts", "oldPath": null, "status": "M",
     "additions": 40, "deletions": 2, "binary": false, "headBlobHash": "git-sha1:..."}
  ],
  "nextCursor": null, "hasMore": false
}
```

- `tree` 条目：`{path,type:"blob|tree",size,gitObjectId,mode,unsupported}`；`file` 返回 `{commit,path,startLine,endLine,totalLines,truncated,content}`；`diff/file` 返回 `{baseCommit,headCommit,path,status,binary,truncated,patch}`；`facts` 条目同 §7.3 的实体结构（不含 fact 正文历史）；`commits/{sha}` 返回 `{sha,message,author,committedAt}`。

### 7.3 版本级 API

| 方法 | 路径 | 说明 | 权限 |
|---|---|---|---|
| GET | `/api/versions/{id}/code/changes?head=` | 版本作用域变更预览（repo/base/head + 文件列表摘要） | 登录 |
| POST | `/api/versions/{id}/code/analyses` | 触发/复用影响分析；`{headCommit?, force?}` | 登录 |
| GET | `/api/versions/{id}/code/analyses/latest` | 最新分析（含候选/unmapped/fact 版本快照） | 登录 |
| GET | `/api/versions/{id}/code/analyses/{analysisId}` | 指定分析详情 | 登录 |
| PATCH | `/api/versions/{id}/code/analyses/{analysisId}/candidates/{candidateId}` | `{selected: bool}` 人工调整 | 登录 |
| GET | `/api/versions/{id}/code/anchors?subjectKind=&subjectId=` | 锚点列表 | 登录 |
| POST | `/api/versions/{id}/code/anchors` | 新建锚点 | 登录 |
| DELETE | `/api/versions/{id}/code/anchors/{anchorId}` | 删除锚点 | 登录 |

- `GET /changes` 为只读预览：实时计算 diff；若已有对应 change set 则返回其 id，否则 `changeSetId=null`（不写库，分析触发时才持久化 change set）。

`POST /analyses` 请求/响应：

```json
// 请求
{"headCommit": "def...", "force": false}
// 202（新建）
{"data": {"id": "1844...", "status": "queued", "analysisVersion": 1, "reused": false}}
// 200（同输入复用）
{"data": {"id": "1844...", "status": "done", "analysisVersion": 1, "reused": true}}
```

`AnalysisDTO`（GET latest/详情）：

```json
{
  "id": "1844...", "versionId": "1844...", "repoId": 3,
  "baseCommit": "abc...", "headCommit": "def...",
  "status": "done", "degraded": false, "analysisVersion": 1,
  "model": "deepseek-chat", "promptVersion": "p4.1",
  "changeSetId": "1844...",
  "changeSummary": "退款接口新增 reason 参数，订单页调用点同步调整",
  "stats": {
    "changedFiles": 12, "analyzedFiles": 10, "cacheHits": 7,
    "facts": 18, "edges": 6, "contracts": 2,
    "evidenceChecks": 30, "evidenceFailures": 1,
    "unmapped": 2, "invalidFacts": 0, "duplicateFacts": 0,
    "unmatchedContracts": 1, "truncated": false
  },
  "factVersions": [{"factKey": "api:POST /api/refund", "version": 3}],
  "candidates": [
    {
      "id": "1844...", "subjectKind": "requirement", "subjectId": "1844...",
      "reason": "changed api POST /api/refund",
      "confidence": "high", "source": "direct",
      "evidence": [{"file": "src/api/refund.ts", "startLine": 80, "endLine": 96,
                    "excerptHash": "sha256:..."}],
      "selected": true
    }
  ],
  "unmapped": [
    {"path": "src/utils/format.ts", "status": "M", "reason": "no_fact_or_anchor",
     "suggestion": "人工确认影响"}
  ],
  "errors": [],
  "createdAt": "2026-10-28T01:00:00Z", "startedAt": "...", "finishedAt": "..."
}
```

- `candidates[].subjectId` 为十进制字符串；`unmapped` 仅详情/latest 返回；`/changes` 返回 `ChangeListDTO`（无候选）。
- `evidence[]` 元素：AI/T1 来源含 `{file,startLine,endLine,excerptHash}`；锚点来源仅 `{file}`（无行区间/哈希）；前端按存在字段渲染。
- `PATCH` 成功返回更新后的候选（200）；未完成分析 409。
- 锚点 DTO：`{id, versionId, subjectKind, subjectId, filePath, startLine?, endLine?, commitSha?, confidence, createdByKind, createdBy, createdAt}`；创建请求 `{subjectKind, subjectId, filePath, startLine?, endLine?}`（`subjectId` 字符串）。

### 7.4 SSE 事件

| type | 触发 | payload 摘要 | refs |
|---|---|---|---|
| `code.changed` | change set 就绪 | analysisId/analysisVersion/base/head/stats | `refs.code` = 变更 path ≤20 |
| `code.impact_computed` | 分析完成/降级 | analysisId/status/degraded/counts | 证据 path 去重 ≤20 |

- 通道固定 `version:{id}`；前端从 `useVersionStream` 过滤 `code.*` 刷新；事件与 §6.9 一致；不新增 SSE 端点。
- repo 级索引状态不广播；前端在代码仓页/代码 Tab 打开期间轮询 `index-runs/latest`（1.5s，最多 60 次后停止并提示手动刷新）。

### 7.5 OpenAPI

- `docs/openapi/api.yaml`（手工维护）新增：`code` tag、§7.2/§7.3 全部 paths、schema（RepoSummary/IndexRun/ChangeList/FileDiff/FactsPage/Analysis/Candidate/Anchor）、reason 枚举、事件类型说明。
- `scripts/acceptance.ps1` 的 OpenAPI needle 检查同步新增：`/api/code/repos/{repoId}/index`、`/api/versions/{versionId}/code/analyses`、`code.impact_computed`、reason `analysis_in_progress`、`anchor_duplicate` 等。
- OpenAPI 校验不得回显真实代码路径/凭据；示例使用占位 path。

---

## 8. Agent 工具与提示词

### 8.1 工具契约（`agent/infrastructure/tools/codeintel.go` 薄壳）

P4 **不新增 Agent profile**；新增以下只读工具并注册到 P1 registry（P3 `code.tree`/`code.read` 保持不变）：

| 工具（registry 名） | wire 名 | 输入 | 输出（摘要） | 上限 |
|---|---|---|---|---|
| `code.repos` | `code_repos` | `{}` | 仓库列表 + 索引状态 | 50 |
| `code.search` | `code_search` | `{repoId, commit?, q, kind?, limit?}` | 实体摘要（path/name/kind/confidence） | 50 |
| `code.diff` | `code_diff` | `{repoId, base, head, limit?}` | 变更文件列表 + stats | 200 |
| `code.facts` | `code_facts` | `{repoId, commit, kind?, path?, limit?}` | 事实摘要 + 证据引用 | 100 |
| `code.routes` | `code_routes` | `{repoId, commit, limit?}` | 路由事实 | 100 |
| `code.apis` | `code_apis` | `{repoId, commit, limit?}` | 接口事实 | 100 |
| `code.analyze` | `code_analyze` | `{versionId, headCommit?, force?}` | 触发/复用分析，返回 status/analysisId；不阻塞等待完成 | 1 |
| `code.impact` | `code_impact` | `{versionId, analysisId?}` | 最新/指定分析的候选摘要（subject/reason/confidence/selected） | 200 |
| `code.anchors` | `code_anchors` | `{versionId, subjectKind?, subjectId?}` | 锚点列表 | 200 |

- 工具名沿用 P3 点号命名；模型侧 wire 名由既有网关映射为下划线（`code.analyze` → `code_analyze`），不改变 P3 映射规则。
- 工具只调用 `codeintel` application 端口，不直接访问 DB/Git/LLM；不导入其他上下文 infrastructure。
- 输出仅摘要与路径；不返回文件正文（`code.read` 已由 P3 提供内容读取且限长）；所有读取受审计与上限约束。
- 输入校验：`repoId`/`versionId` 合法；`commit` 40hex；`limit` 1–200；越界由服务层 clamp。
- 注册顺序：在 `cmd/eztest` 构建 registry 前 append；`GET /api/agents/tools` 会自动暴露新工具（相关测试同步更新）。
- P3 profile 的 `allowed_tools` **不变**；P5 接入时再配置。`code.analyze` 异步工具必须返回"已触发"而非等待完成，避免占用 Agent run。

### 8.2 工具错误与降级

- 参数错误 → `invalid_argument` + reason（如 `invalid_repo_id`）；资源不存在 → `not_found`；Git/LLM 不可用 → `unavailable`（retryable）；不暴露内部路径/命令输出。
- 工具超时沿用 `EZTEST_AGENT_TOOL_TIMEOUT_MS`；`code.analyze` 只做触发，不因分析未完成而超时。
- 工具调用审计沿用 P1 executor（tool/status/duration/run/session）；不额外记录代码正文。

### 8.3 T1 文件分析提示词（`p4.1`，原文）

**System**：

```text
你是 EzTest 的只读代码分析器。用户会给出一个源码文件（某次 Git 提交中的版本）及其路径。
你的任务：提取与页面/路由/接口/组件/依赖相关的结构化事实，并概括该文件的变更语义。
规则：
1. 只依据给定文件内容判断；无法确定的事实不要输出，禁止猜测或编造。
2. 每条事实必须能定位到文件中的证据行（1-based 闭区间）；证据必须来自给定文件。
3. kind 只能是 route/page/api/client_call/component/module/symbol 之一；不假设语言或框架。
4. 路由/接口/调用点必须使用代码中实际出现的路径与方法字符串；方法统一大写。
5. 置信度：证据充分且直读为 high；由代码推断为 medium；不确定为 low。
6. changeSummary 说明"这个文件改了什么/负责什么"，不臆造提交信息。
7. 只输出一个 JSON 对象，符合用户给出的 schema；不要 Markdown 代码围栏或解释文字。
```

**User 模板**：

```text
仓库：{repoKey}（{repoName}）
提交：{commit}
文件：{path}
文件总行数：{totalLines}
文件内容（行号见左侧，仅用于定位证据）：
<file-content>
{编号行文本，最多 2000 行}
</file-content>

输出 JSON schema：
{
  "changeSummary": "string",
  "facts": [{
    "kind": "route|page|api|client_call|component|module|symbol",
    "name": "string",
    "path": "{path}",
    "startLine": 1, "endLine": 1,
    "signature": "string",
    "confidence": "high|medium|low",
    "summary": "string",
    "evidence": [{"file": "{path}", "startLine": 1, "endLine": 1}]
  }],
  "edges": [{
    "from": {"kind": "component", "name": "string"},
    "to": {"kind": "route", "name": "string"},
    "rel": "contains|imports|defines|calls|renders|route_to_api",
    "confidence": "high|medium|low",
    "evidence": [{"file": "{path}", "startLine": 1, "endLine": 1}]
  }]
}
```

### 8.4 影响推理提示词（`p4.1`，原文）

**System**：

```text
你是 EzTest 的只读变更影响分析器。输入是一次代码变更的结构化事实摘要与版本需求清单。
任务：判断哪些需求可能受该变更影响。
规则：
1. 只依据给定事实与需求文本推理；不要引用未给出的代码或需求。
2. 每条受影响需求必须给出 reason（≤300 字）与证据（只能是给定变更文件/事实证据中的 file + 行号区间）。
3. 无法判断时返回空数组；不确定时 confidence=low。
4. requirementId 必须使用给定清单中的十进制字符串 ID，不得编造。
5. 只输出一个 JSON 对象，符合 schema；不要 Markdown 或解释。
```

**User 模板**：

```text
版本需求清单（id | 标题 | 描述摘要）：
{requirements}

变更文件（path | status）：
{changedFiles}

变更事实（kind | name | path | summary | confidence | evidence）：
{facts}

变更摘要：
{changeSummary}

输出 JSON schema：
{
  "affected": [{
    "requirementId": "1844...",
    "reason": "string",
    "confidence": "high|medium|low",
    "evidence": [{"file": "string", "startLine": 1, "endLine": 1}]
  }]
}
```

### 8.5 安全与边界

- 只读：不执行代码、不联网、不改工作树、不切换分支；Git 命令 argv 化。
- 输入最小化：只送变更文件（≤20、≤64 KiB/文件），不整仓外发。
- 不记录代码正文/提示词/模型响应到日志、事件、审计；错误只含 path/行号/reason。
- 代码可能含密钥：报告/候选/证据只引用 `file:line` 与摘录哈希，不批量导出代码；UI 读取受权限与审计约束。
- 普通用户可读取代码内容（既有登录用户权限）；仓库配置/索引 admin（蓝图 §10.2）。

---

## 9. 前端设计

### 9.1 路由与信息架构

- 版本详情 `/versions/[id]?tab=code`：「代码与变更」Tab 由占位替换为真实 `CodeChangesTab`；Tab 顺序/标签不变。
- 代码仓管理 `/code-repos`：保留 P2 配置能力，新增索引状态徽标与「刷新索引」按钮（admin）；移除「未索引（P4）」占位文案。
- 不新增独立路由；不引入 React Flow/表单库/状态库；HTTP 一律 `api-client`，SSE 一律 `sse-client`/`useVersionStream`。

### 9.2 组件与交互

| 组件 | 职责 |
|---|---|
| `CodeChangesTab` | 页面容器：加载版本绑定→repo 信息→latest 分析；布局为「左：变更文件/事实；右：影响面/未映射/锚点」；状态机（未绑定/未索引/无变更/运行中/完成/降级/失败） |
| `CommitHeader` | 展示 `repo@base → head`、分析版本/时间、状态徽标；「刷新索引」（admin）、「重新分析」（force 二次确认）、「开始分析」（无分析时） |
| `ChangedFiles` | change set 文件列表（status/additions/deletions/binary 标记）；点击打开 `DiffDrawer`；行内「关联需求」打开 `AnchorDialog` |
| `DiffDrawer` | 按需拉取单文件 unified diff；等宽字体、+/- 行着色、截断/二进制提示；ESC 关闭、焦点管理 |
| `FactsPanel` | AI/T0 事实按 kind 分组（route/page/api/client_call/component/module/symbol）；置信度徽标；low 灰显并标「低置信线索」；证据链接 |
| `EvidenceLink` | `file:line` 链接 → 打开代码片段抽屉（`/file` 行窗口，高亮证据行区间） |
| `ImpactPanel` | 需求/应用模型候选分组；复选框（PATCH 保存）；reason/source/confidence/evidence 展示；low 默认未选且可手动选中；保存失败回滚 + 提示 |
| `UnmappedChanges` | 未映射文件列表（reason/suggestion）；截断时提示总数 |
| `AnchorDialog` | 选择版本需求 + 文件/行区间创建锚点；展示当前主体已有锚点并可删除 |
| `RunStatus` | queued/running 轮询（1.5s，≤60 次）；degraded 横幅「AI 不可用，仅文件级结果」；failed 展示 reason 与重试入口 |
| `EmptyState` | 未绑定代码仓（引导去配置/绑定）、无变更、未索引、无分析、AI 未配置等空态；不展示假数据 |

- 修复 P3 占位测试：`VersionTabs` 增加 `code` slot 并移除 code 占位；`VersionDetailPage` 挂载真实组件；更新 `VersionDetailPage.test.tsx`、`e2e/versions.spec.ts`、`e2e/code-repos.spec.ts` 中与 P4 占位/未索引相关断言。
- 代码仓页：`lastIndexedAt` 非空显示时间与 commit；空显示「未索引」；「刷新索引」点击后显示 queued/running 并轮询，完成后刷新列表；普通用户按钮禁用/隐藏（后端 403 兜底）。
- 代码 Tab 的「批次时间线」「受影响用例」区块 P4 **不渲染**（P6/P7 接入）；不显示空假区块，仅在页面说明中标注归属阶段。

### 9.3 数据与状态

- `features/code-intel/api.ts`：`getRepos`、`getIndexRun`、`startIndex`、`getChanges`、`getDiffFile`、`getFileWindow`、`getFacts`、`createAnalysis`、`getLatestAnalysis`、`getAnalysis`、`patchCandidate`、`listAnchors/createAnchor/deleteAnchor`；全部经 `api-client`，错误转 `ApiError`。
- 类型：`repoId: number`；analysis/anchor/candidate/entity ID、`subjectId` 一律 `string`；禁止 `Number(id)`；时间用 ISO 字符串。
- 轮询 hook：`useAnalysisPolling(versionId, activeId)` 仅在 queued/running 时轮询；完成/失败/卸载即停；页面隐藏（`document.hidden`）暂停并在恢复时立即拉取一次。
- 版本流：`useVersionStream` 已有；`CodeChangesTab` 订阅并过滤 `code.changed`/`code.impact_computed` → 重取 latest（去抖 300ms）；与其他 Tab 的刷新互不影响。
- 初始化顺序：GET `/code/changes` 获取 repo/base/head（失败则展示未绑定/无基线空态）→ GET latest 分析 → 若有运行中则轮询。
- 乐观更新：候选勾选先本地切换，PATCH 失败回滚并 toast；同一候选进行中禁用重复点击。

### 9.4 空/错/加载与可访问性

- 加载：骨架屏；≥300ms 才显示避免闪烁；错误：`ApiError.reason` 映射中文提示；401 由 api-client 统一跳登录。
- 空态：未绑定仓、无 baseline、无变更、未索引、无分析、AI 未配置/降级分别有不同的引导文案与操作。
- 可访问性：复选框有 `aria-label`；抽屉 `role="dialog"` + 焦点捕获 + ESC；置信度/状态徽标有文本而非仅颜色；对比度满足 AA；键盘可完成「开始分析/调整候选/关联需求」。
- 响应式：≥1280 双栏；<1024 单栏纵向堆叠；diff 抽屉全宽。

### 9.5 前端测试

- 组件：状态机渲染（未绑定/未索引/无变更/运行中/完成/降级/失败）、候选勾选/回滚、证据链接、unmapped、空态、ID 类型不转换。
- API：mock `api-client` 断言路径/方法/参数（repoId number、id string、selected bool）。
- E2E 断言见 §10.5；更新 P3 占位断言。

---

## 10. 测试与验收

### 10.1 分层矩阵

| 层 | 范围 | 工具/命令 |
|---|---|---|
| 单元 | gitx 解析（tree/diff/status/path）、证据哈希/行切分、contract 归一化、fact_key、JSON 解析、候选合并排序、缓存键、状态机、降级、ID 序列化 | `go test ./internal/codeintel/...` |
| 集成 | 迁移 9 表、唯一约束、change set 复用、索引 run、分析 run（fake LLM）、事实/版本物化、候选/人工调整、锚点、权限、SSE、恢复 | `go test -tags=integration ./internal/codeintel/integration/...`（`testutil.OpenTestDB`） |
| 前端 | 组件/API/hook | `pnpm vitest run src/features/code-intel` |
| E2E | fixture 仓库 + fake LLM 全链路 | `scripts/e2e.ps1`（Playwright） |
| 验收 | fake 全量门禁 + gated 真实 LLM（fixture + EzTest 自身 diff）+ 截图 | `scripts/test.ps1`、`scripts/acceptance.ps1`、`scripts/acceptance-codeintel-llm.ps1` |

### 10.2 Fixtures

- **fixture 仓库**（`.tmp/p4-e2e-repo`，由 `scripts/fixture-codeintel-repo.ps1` 创建；gitignored）：
  - 初始 commit：`src/api/refund.ts`（`client_call POST /api/refund`）、`src/router.ts`（`route:/orders`）、`src/pages/OrdersPage.tsx`（component `OrdersPage`）、`server/refund.go`（`api POST /api/refund`）、`src/utils/format.ts`、`assets/logo.png`（二进制）。
  - 第二个 commit：refund 调用/接口增加 `reason` 参数（跨栈契约两侧同时变更）、OrdersPage 组件变更、新增 `route:/orders/refund`、修改 `src/utils/format.ts`（用于 unmapped）、删除 `src/legacy/old.ts`、更新二进制（unsupported）。
  - 文件数 ≤15，便于断言与真实 LLM 冒烟；Git 用户名/邮箱在脚本内固定，commit 时间固定（确定性）。
- **P2 资源**：E2E 创建 system + app model 节点（page:/orders、route:/orders、api:POST /api/refund、component:OrdersPage）+ code repo 配置；普通用户账号。
- **fake LLM**（`scripts/fake-llm.cjs`）新增 P4 场景：
  - 识别系统提示词 `EzTest 的只读代码分析器` → 按文件 path 返回确定性 facts/edges（含证据行号；证据区间与 fixture 内容一致）；
  - 识别 `EzTest 的只读变更影响分析器` → 默认返回空数组（仅验证确定性候选）；`EZTEST_FAKE_LLM_P4_MODE=impact-ai` 时按需求标题标记返回受影响 requirement id（用于 AI 候选/证据校验场景）；
  - 记录请求到 `.tmp/fake-llm-requests.jsonl`（用于缓存命中/失败断言）；
  - 环境开关 `EZTEST_FAKE_LLM_P4_MODE=ok|error|partial`（error=500，partial=部分文件返回非法 JSON）用于降级/部分失败测试。
- **真实仓库验收**：克隆 EzTest 到 `.tmp/p4-real-repo`（或用户指定路径），创建验收分支指向真实 base commit；配置 repo 指向克隆；版本绑定该分支（baseline=base）；分析请求显式 `headCommit`=真实后续 commit（base 后代，变更文件 ≤20）。所选 commit 对与文件数记录在 `acceptance.md`。
- **集成测试 Git**：`t.TempDir()` 内 `git init` + 固定作者/时间生成双 commit；复用 P3 gitread 测试的 Git 可用性假设。

### 10.3 后端测试清单

- gitx：tree 过滤/排序/分页、blob hash、diff 状态（A/M/D/R/C/T）、-z 特殊文件名、unified diff 截断/二进制、file 行窗口/UTF-8/大小上限、路径穿越/绝对路径/symlink/submodule、HEAD 解析、base 后代校验、超时。
- 证据：CRLF 归一、行号边界、哈希匹配/不匹配、文件删除、超大/二进制、`MaxExcerptLines`。
- 契约：method/path 归一（绝对 URL/query/动态段/UUID/重复斜杠/末尾斜杠/大小写）、匹配/不匹配/截断。
- 缓存与版本：键命中/未命中、force 跳过、模型/prompt 变化 miss、事实 body 相同复用/不同递增、旧版 supersede、同 run 重复 fact_key 合并。
- 分析：复用（同输入返回既有、reused=true）、force 新版本、并发 409、队列满 429、降级（未配置/LLM 500/timeout/parse 失败）、部分失败 degraded、空变更、base==head、无效 head。
- 影响：直接锚点、应用模型精确匹配、契约传播、需求关系一跳（不递归）、AI 候选证据校验/未知需求丢弃/低置信默认不选、去重排序、unmapped 五类原因（含 `unmatched_contract`）、候选 PATCH 权限/持久化/未完成 409。
- 锚点：三类主体校验/重复/删除/归属/case 拒绝/路径不存在。
- 索引：run 状态机、truncated、last_indexed_at 经 resource 端口写入（不直接写表）、索引进行中 409。
- HTTP：401/403/404/409/422/429 reason、ID 字符串序列化、分页 cursor、OpenAPI 关键 path、SSE 事件 payload/refs 上限。
- 组装：codeintel 依赖注入、工具注册顺序/数量、`GET /api/agents/tools` 暴露、P3 工具不受影响。
- 恢复：启动将遗留 queued/running 置 failed(interrupted)。

### 10.4 前端测试清单

- Tab 真实现替换占位（更新 P3 测试）；状态机六态；候选勾选/回滚；证据链接/抽屉；unmapped；锚点创建/删除；ID 类型断言；轮询停止条件；SSE 事件触发刷新；权限（普通用户索引按钮隐藏/禁用）；空态文案。

### 10.5 E2E

1. `versions-code.spec.ts`：配置 fixture 仓库 → 创建版本并绑定（system/repo/branch）→ 打开「代码与变更」→ 触发索引（等待 done）→ 查看变更文件与 diff → 开始分析（fake ok）→ 候选（需求/应用模型）含 reason/confidence/evidence → 勾选/取消并刷新持久化 → unmapped 展示 → SSE 驱动更新 → 无变更/未索引空态。
2. `versions-code-cache.spec.ts`：同版本同 commit 再次分析 → `reused=true` 且 fake LLM 请求数不增；另建一个绑定相同 repo/base 的版本并分析同一 head → `stats.cacheHits>0`（文件级缓存生效、无新增 LLM 调用）；`force=true` → `analysisVersion=2`、旧分析仍可 GET 且候选不变；prompt 版本变化场景（集成测试）→ 新 fact 版本且旧版 `superseded_at` 非空。
3. `versions-code-degraded.spec.ts`：`EZTEST_FAKE_LLM_P4_MODE=error`（或删除默认 LLM 配置）→ 分析 `degraded=true`、HTTP 2xx、UI 横幅、直接锚点/应用模型候选仍可见、unmapped 含 `ai_unavailable`；恢复后可 force 重跑。
4. `code-repos-index.spec.ts`：普通用户看不到/不能触发索引（403 兜底）；管理员刷新索引状态从「未索引」→ running → done + 时间；P2 配置校验不回归。
5. `versions-code-anchors.spec.ts`：变更文件行内「关联需求」→ 创建锚点 → 重跑分析出现 direct 需求候选 → 删除锚点 → force 重跑后该候选消失。
6. 回归：P3 全部 E2E 通过（占位文案断言已更新）。

### 10.6 DoD 映射

| P4 退出标准（implementation-phases §2-P4） | 验证 |
|---|---|
| 任意语言仓库可做文件级 diff；AI 能识别变更涉及的接口/路由/页面/关键逻辑并给出证据 | fixture（TS+Go）E2E + gitx 单测 + 真实 LLM 验收 |
| 同 commit 重跑影响分析结果复用缓存且一致；AI 重分析产生新版本不覆盖 | `versions-code-cache.spec.ts` + 集成测试（reused/force/规范化 JSON 对比） |
| 影响结果带原因/置信度/证据，可人工勾选；未映射变更可见 | E2E + 候选 PATCH 集成测试 + unmapped 断言 |
| AI 失败时主流程降级不阻塞 | `versions-code-degraded.spec.ts` + 集成测试（degraded/T0 候选/2xx） |

| CI-M0 验收场景（`code-intelligence.md` §11.2） | P4 映射 |
|---|---|
| 1 任意语言仓库只读挂载：文件树/commit/文件级 diff | T0 API + E2E（fixture TS+Go） |
| 2 未知语言变更 AI 能识别接口/路由/页面并给证据；无法识别明确标注 | T1 集成 + 真实 LLM 验收（证据哈希） |
| 3 接口/路由变更 → 受影响候选与原因；低置信人工确认 | 影响分析集成 + E2E 勾选 |
| 4 未映射变更显式展示 | unmapped 断言 |
| 5 同 commit 复用且一致；重分析新版本不覆盖 | 缓存/版本测试 |
| 6 用例/问题可显示并跳转 code_ref | **P4 仅锚点 + 证据跳转；case/issue 归 P5/P6（用户 Q1 决策）** |
| 7 AI 不可用 → T0 + 人工确认，不阻塞 | degraded E2E |
| 8 批次执行/证据/报告带 base/head 与 fact 版本 | **P4 落库 base/head/fact 版本快照；批次/报告 UI 归 P6/P7** |
| 9 AI 接口/路由事实通过 file+line+摘录哈希校验；不通过降线索 | 证据校验单测/集成 + 真实 LLM 验收 |

### 10.7 真实人工验收（Agent 代替用户执行）

**前置**：`scripts/dev-api.ps1` + `scripts/dev-web.ps1`；PostgreSQL 5433；真实 `.env`（LLM 三键）；真实 LLM gate `EZTEST_RUN_REAL_LLM=1`；迁移 16 条；OpenAPI 已同步。

**步骤（按真人操作路径）**：

1. 管理员登录；`/code-repos` 配置真实仓库（EzTest 克隆 + 验收分支），保存校验通过；点击「刷新索引」→ 状态 running→done，显示 commit 与时间。
2. 创建版本：绑定 system（含 app model 节点）+ 代码仓 + 分支；录入 3 条需求与 1 条关系；打开「代码与变更」查看 T0 变更预览后，为需求创建 1–2 个锚点（无需等待 AI 分析）。
3. 触发影响分析：head=真实后续 commit；等待完成；核对：
   - 变更文件列表与 `git diff --name-status` 一致（抽查 3 个）；
   - AI 事实（路由/接口/页面/组件）带 confidence；逐条点击证据，`file:line` 内容与 commit 内容一致；
   - 需求/应用模型候选带 reason/置信度/证据；若出现 low 候选，默认未选且可勾选（不强制出现）；
   - unmapped 至少包含 1 条真实未映射文件；无静默遗漏；
   - SSE 在分析完成时自动刷新（无需手刷）。
4. 缓存一致性：再次 POST（不带 force）→ `reused=true`、同 analysisId、候选规范化 JSON 完全一致；另建同绑定版本分析同一 head → `stats.cacheHits>0`（文件级缓存生效）；`force=true` → `analysisVersion+1`、旧分析仍可读取且结果不变。
5. 降级：临时将默认 LLM 配置改为不可用地址（管理员设置页）→ force 分析 → `degraded=true`、HTTP 2xx、UI 横幅、T0 直接候选仍可用；恢复配置后 force 重跑成功。
6. 权限：普通用户可读代码/触发分析/调候选；不能触发索引（403）；未登录 401。
7. 记录：截图存 `.tmp/p4-acceptance/`；命令/exit/计数/结论写 `.superpowers/sdd/<plan>/acceptance.md`（追加式，单列 Date/HEAD/Status）；不粘贴代码正文/密钥；fake E2E 截图不得冒充真实 LLM 验收。

**门禁顺序**：`scripts/test.ps1` → `scripts/e2e.ps1` → `scripts/acceptance.ps1` → gated 真实 LLM 脚本 → 人工验收 → 代码审查 → 合回 `dev`（本地 ff，不 push）。

---

## 11. 风险与降级

| 风险 | 处理 |
|---|---|
| AI 事实质量不稳定/编造 | 证据存在性校验 + 置信度分级 + low 不进自动影响 + 人工勾选；真实 LLM 验收抽检证据；CI-M1 评测 |
| 大仓/大 diff 超时或爆内存 | 文件数/字节/摘录/索引/树分页上限；超限 `truncated` + skipped 统计；整体 timeout 180s；只分析变更文件 |
| Git/路径/Windows 差异 | argv 无 shell、slash 归一、根内校验、symlink/submodule 拒绝、特殊文件名/rename -z 测试 |
| 二进制/非 UTF-8/CRLF | 明确 `unsupported_content`；摘录按 LF 归一再哈希；单测覆盖 |
| 异步 worker 单实例/重启丢任务 | DB 状态权威；202+轮询；启动恢复 queued/running→failed(interrupted)；允许重触发；单实例写入假设 |
| 缓存/版本膨胀 | fact 版本仅在 body 变化时递增；缓存按 commit 不可变；索引与查询索引；后续 GC 策略（P8） |
| 事件上限/慢消费者 | 事件只放 id/计数/路径（≤20）；`summary≤500`、`detail≤64KiB`；hub 慢消费者断开不影响分析落库 |
| P2 number 与 D29 string 混用 | DTO/前端类型分治（repoId number、codeintel/requirement/app_model subject string）；序列化测试 |
| 迁移顺序/checksum | 只追加 order.txt 第 15/16 位；不改旧迁移；验收迁移计数 14→16 |
| P3 工具/profile 回归 | 不新增 profile、不改 `allowed_tools`；`code.tree`/`code.read` 不动；新工具用未占用名；registry/工具列表测试 |
| 占位测试/OpenAPI 联动变红 | 计划内任务同步修改 `VersionDetailPage.test.tsx`、`e2e/versions.spec.ts`、`e2e/code-repos.spec.ts`、OpenAPI needles |
| 真实 LLM 验收成本/波动 | gated 脚本 + 明确 commit 对；证据/缓存/降级断言优先于文本质量；失败记录原因并可重跑；fixture 兜底 |
| 影响漏报/误报 | `unmapped_changes` 显式；候选带 reason/confidence/evidence；人工可调；P8/CI-M1 度量 |
| 工作树 dirty / 执行期间代码变化 | 以 commit 为准并提示 dirty；head 每次实时解析；D23：不自动打断，提示用户决定新分析 |
| 代码含密钥/外发风险 | 只送变更文件；不整仓外发；日志/事件/审计不含正文；证据只引用 file:line+哈希 |
| 单版本并发分析 | 同版本 queued/running → 409；`analysis_version` 唯一约束；缓存 upsert 幂等 |
| OpenAPI 手工漂移 | 验收 needle + 规格任务显式同步；新增 path/schema/reason 均列入 plan |

---

## 12. 交付物

1. 后端 `codeintel` 限界上下文（domain/application/infrastructure/interfaces/integration）与 `cmd/eztest`/`server`/`config` 装配。
2. 两个迁移（9 表）+ `order.txt` 追加；P2 `MarkIndexed` 应用端口。
3. 仓库级/版本级 HTTP API、DTO、错误 reason、分页、权限、审计。
4. T0 Git 适配器、T1 分析/证据/缓存/版本化、契约匹配、影响分析、锚点、异步 worker。
5. `code_*` 只读工具（9 个新工具；复用 P3 `code.tree`/`code.read`）。
6. 前端 `features/code-intel` + 版本 Tab 真实现 + 代码仓索引状态；P3 占位测试更新。
7. `scripts/fixture-codeintel-repo.ps1`、`fake-llm.cjs` P4 场景、`e2e.ps1` 注入、`acceptance.ps1` needle、`acceptance-codeintel-llm.ps1` gated 验收。
8. `docs/openapi/api.yaml`、`.env.example`、README、`Agent.md §6.1`、`system-blueprint.md`（D32/D33）、`code-intelligence.md`（P4 实施细化）。
9. 单元/集成/前端/E2E 测试与 `acceptance.md` 真实验收证据。

---

## 13. 设计变更与文档同步

### 13.1 D32 原文（追加至蓝图 §3.2）

> **D32 P4 代码理解与影响分析 v1**：新增 `codeintel` 限界上下文（DDD 四层，`Agent.md` §6.1 登记），复用 P2 `code_repositories`（BIGSERIAL，API number），新增 9 表（雪花 ID：`code_change_sets`/`code_index_runs`/`code_entities`/`code_edges`/`code_fact_versions`/`code_analysis_cache`/`code_analyses`/`code_impact_candidates`/`code_anchors`），`repo_id`/`version_id`/`requirement_id`/`app_model_id` 均为逻辑引用、不建跨上下文 FK。T0 只读 Git 文件级能力（tree/blob hash/文件级 diff/按需 unified diff/行读取/commit/HEAD；不含 parser）；T1 服务化：应用服务直调 P0 LLM 网关（chat 协议 + JSON 提示词 + 严格解析 + 证据校验），异步 run（202/轮询/SSE；单实例进程内 worker，默认并发 1）；**不新增 Agent profile**，仅注册 `code_*` 只读工具薄壳（P3 `code.tree`/`code.read` 契约不变）。证据必须为 `path + start/end line + 摘录 sha256`，按 **commit 内容**校验；失败降为 low 线索，不参与自动影响。影响分析 v1 输出受影响**需求/应用模型**与 `unmapped_changes`；`affected.cases` 结构保留、P5 接入（P4 无 case/issue 实体）。跨栈契约存 `code_edges.rel='route_to_api'`（method + 归一化 path）。API 为 `/api/code/*`（仓库级）与 `/api/versions/{id}/code/*`（版本级）；仓库配置/索引 admin，其余登录用户。版本分析完成发 `code.changed`/`code.impact_computed`（`version:{id}`，仅摘要）。AI 不可用 → `done + degraded`，T0-only，HTTP 2xx 不阻塞。明确不做 parser/符号级/字段级分析、用例锚点实体、批次、全仓 AI 分析、线上仓、monorepo 子项目配置。

### 13.2 D33 原文（追加至蓝图 §3.2）

> **D33 P4 缓存与版本化**：文件级分析缓存键 = `(repo, commit, path, file_hash, model, prompt_version)`（file_hash 为 `git-sha1:<40hex>` 或内容 `sha256:<hex>`）；命中不调 LLM。事实按 `fact_key` 版本化：`code_fact_versions` 保存历史，body 语义相同则复用，不同则 `version+1` 并写旧版 `superseded_at`；`code_entities` 仅保存当前版本。影响分析同 `(version, base, head, model, prompt_version, fact 版本集)` 默认复用（`reused=true`）；`force` 或模型/提示词变化新建 `analysis_version`，不覆盖旧记录。候选集合/顺序/默认勾选确定性：source 优先级 direct > contract > relation > ai，再按 confidence、subject kind/id；low 默认不选中但可人工选中。证据摘录哈希 `sha256`，按 commit 内容复算；同 commit 重跑影响结果一致。P4 不提供事实人工修正与版本对比 UI（CI-M1）。

### 13.3 同步修改表

| 文档 | 变更 |
|---|---|
| `docs/design/system-blueprint.md` | §3.2 追加 D32/D33；§7.3 补充 P4 实施落点（影响对象/异步/API 作用域） |
| `docs/design/code-intelligence.md` | 头部状态指针；§13 DDL 按 D29/D32 定稿（雪花、9 表、无跨上下文 FK）；新增 §14「P4 实施细化」（证据按 commit、影响对象范围、缓存/版本语义、API/事件、降级）；§11 CI-M0 与 P4 映射说明 |
| `Agent.md` | §6.1 目录树新增 `backend/internal/codeintel/` 与 `frontend/src/features/code-intel/`；补 P4 段落（四层、端口、API 前缀） |
| `.env.example` | 新增 §4.1 八个 `EZTEST_CODEINTEL_*` 键与注释 |
| `docs/openapi/api.yaml` | 新增 §7 全部 path/schema/reason/事件类型；占位示例 |
| `README.md` | P4 功能/运行/索引/验收命令；代码仓只读挂载说明 |
| `scripts/acceptance.ps1` | 迁移计数 16、P4 OpenAPI needle、事件/reason 检查 |
| `docs/design/implementation-phases.md` | 不改（计划性文档，无设计权威）；P4 进度不写入 |

---

## 14. 明确不做（再确认）

1. parser/静态解析、符号表、符号级调用链、字段级分析。
2. 用例/问题实体、`code_ref` 页面跳转（P5/P6/P7）；`affected.cases=[]` 仅结构预留。
3. 批次、执行、暂停/恢复、实时播报、报告（P6/P7）；批次时间线与「重跑受影响用例」不在 P4 UI。
4. 事实人工修正、事实质量指标/评测（CI-M1）。
5. 全仓 AI 分析、全仓索引 UI、全文检索（D24）。
6. 线上仓、分支切换、工作树修改、执行代码。
7. monorepo 子项目配置/自动识别、网关前缀推断、跨语言 parser。
8. 多实例 worker/任务跨进程恢复。
9. 新 Agent profile、修改 P3 `code.tree`/`code.read` 契约。
10. 视觉断言、CI/工单集成（后续阶段）。

---

## 15. 已知假设

1. **单实例** API 进程：worker 进程内运行；多实例部署与任务恢复留 P8。分析/索引任务在崩溃后按 §6.8 标 failed，需人工重触发。
2. 代码仓位于 API 进程可读的本地文件系统；Windows 开发与 Linux 容器路径均支持；Git 仓库使用 SHA-1 对象（40hex）；SHA-256 仓库不支持（后续）。
3. LLM 为 chat completions 兼容接口，能按提示词返回 JSON 文本；无结构化输出/工具调用依赖；默认 LLM 配置存在且可用（否则 degraded）。
4. 真实仓库验收使用 EzTest 克隆 + 真实 commit 对（base 后代，变更文件 ≤20）；具体 commit 对记录在 `acceptance.md`；若真实 LLM 不可用则按 gated 规则 SKIP 并记录，fixture fake 验收仍需通过。
5. P4 无 case/issue 实体：影响对象为需求/应用模型/unmapped；P5 接入 case 锚点与用例候选。
6. 应用模型节点可能为空（P2 未配置）；此时应用模型候选为空但分析成功；需求影响仍可用。
7. P2 `code_repositories.id` 保持 BIGSERIAL/JSON number；P4 不回溯改造；codeintel 自有 ID 为雪花/字符串。
8. 跨栈匹配仅精确 method+归一化 path；网关前缀/命名差异导致的不匹配显式进 unmapped/stats（P4 限制）。
9. 证据校验依赖 Git 内容读取；超大/二进制文件无法作为证据来源，相关事实降为 low。
10. 真实 LLM 输出存在不确定性：验收以证据可核验、缓存/版本/降级行为为主，不以"文本完全一致"为通过条件。
11. 前端不新增依赖；diff 以原生文本渲染；大 diff/文件列表截断由后端控制。
12. 本地验收使用 `.tools` 便携 Go/PostgreSQL；Docker Compose 仅静态校验（本机无 Docker）。

---

*文档版本：v1.0（2026-10-28）；与 `system-blueprint.md` v2.2 配套；新增 D32/D33。执行中如调整范围，先更新本规格与蓝图并记录原因。*
