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
│     ├─ identity/                    # 限界上下文：用户/认证
│     │  ├─ domain/                   # 实体、值对象、仓储接口、领域错误
│     │  ├─ application/              # 用例/命令/查询
│     │  ├─ infrastructure/           # postgres repo、bcrypt、token 生成
│     │  └─ interfaces/httpapi/       # handler、DTO、路由
│     ├─ eventing/                    # 限界上下文：事件/SSE
│     ├─ llm/                         # 限界上下文：LLM 配置/网关/模型目录
│     ├─ storage/                     # 限界上下文：对象存储
│     └─ audit/                       # 限界上下文：审计
├─ frontend/
│  ├─ src/app/                        # Next.js App Router，只做页面组装
│  ├─ src/features/{auth,users,llm-settings,shell}/
│  ├─ src/components/ui/              # 共享 UI 原语
│  ├─ src/lib/                        # api-client / sse-client / auth-storage / utils
│  ├─ src/hooks/  src/navigation/  src/styles/
│  └─ package.json
├─ deploy/                            # Dockerfile.api / Dockerfile.web / docker-compose.yml
├─ scripts/                           # dev/db/test/e2e PowerShell 脚本
├─ docs/openapi/                      # OpenAPI 骨架
├─ .env.example  .gitignore
└─ .tools/                            # 便携 Go/PostgreSQL（gitignored）
```

每个限界上下文**必须**四层齐全：`domain/`、`application/`、`infrastructure/`、`interfaces/httpapi/`。上下文名使用小写单数（`identity`、`eventing`、`llm`、`storage`、`audit`），不得新建 `utils/`、`common/`、`helpers/` 之类杂物目录；共享纯逻辑放 `internal/platform/`。

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

---

## 7. 本文件边界

- **不是设计文档**：设计事实、决策、原则、不变量、约束一律从主蓝图读取。
- **不是进度文档**：阶段进度、任务状态、分支/环境状态不属于本文件。
- **不是规范全文**：实现细节以详细设计文档为准。
- **不替代阅读**：本文件只负责指路与权威判定；动手前仍需按 §1–§2 实际查阅并给出依据。
