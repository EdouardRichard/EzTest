# EzTest 代码智能设计 v0.4（语言无关：AI 理解为主，不设计 parser）

> **状态**：draft（与 system-blueprint v2.2 配套）
> **核心原则**：**不限定被测项目的语言/技术栈；不设计 parser/静态语言解析器**。代码理解完全由 AI 承担（读文件/diff/上下文，产出结构化事实 + 证据 + 置信度）；系统只保留语言无关的 **T0 通用 Git 层** 作为确定性兜底。
> **真源**：管理员配置的**本地 Git 仓库**（只读挂载、固定分支、不联网）。不接线上代码仓。
> **目标**：让 AI 理解"代码现状"并据此设计用例、做变更影响分析、定位问题代码位置；同时保证低 token、可复现、可追溯。
> **关联**：`system-blueprint.md`（架构/生命周期）、`frontend-design.md`（代码与变更 Tab）。

---

## 0. 结论摘要（TL;DR）

1. **语言无关**：不维护语言白名单、不做技术栈枚举、**不设计 parser**；任何语言/框架的代码都走同一套流程（T0 通用 Git 层 + T1 AI 代码理解）。
2. **两层代码智能**：
   - **T0 通用层（确定性、语言无关）**：文件树、文件元数据、commit、`git diff`（文件级变更）、锚点——任何语言都成立。
   - **T1 AI 代码理解层（核心，语言无关）**：AI 读变更/相关文件，产出结构化代码事实（页面/路由/接口/组件/符号/依赖/变更语义）+ 证据（file:line）+ 置信度；按 commit/文件哈希缓存，可校验、可版本化。
3. **影响分析 = 确定性候选集（文件 diff + 锚点）+ AI 影响推理（读 diff/代码）**；输出带原因与置信度，人工可调整；无法归因的变更显式进 `unmapped_changes`。
4. **事实必须有证据**：AI 产出的"事实"必须能在代码中找到证据（文件/行/片段摘录哈希）；无证据只能算低置信线索，不参与自动影响判定。
5. **可复现**：批次固定 commit；代码事实带版本（commit + 文件哈希 + 模型/提示词版本）；同一 commit 复用缓存；AI 重分析产生新版本而非覆盖。
6. **可追溯**：用例/问题/报告可挂 `code_ref`（path:line / 路由 / 接口）；证据引用代码片段。
7. **AI 不可用时降级**：仅 T0 文件级影响候选 + 人工确认；主流程不阻塞。

---

## 1. 目标与非目标

### 1.1 目标

- 语言无关地理解被测仓库：页面/路由/接口/组件/关键逻辑/依赖关系（由 AI 判断，系统不设语言门槛）。
- 建立需求/用例/问题与代码位置的**锚点**，支撑可追溯。
- 代码变更（commit diff）→ 影响面 → 受影响用例/需求，供增量批次与回归选择。
- 为 Agent 提供低 token、结构化、可引用的代码事实；按需下钻源码。
- 可复现、可审计（commit 固定、事实版本化、操作审计）。

### 1.2 非目标

- ❌ 不限定被测项目语言/技术栈，也不维护语言白名单。
- ❌ **不设计 parser/静态语言解析器**、不做语法树/符号表解析；语言理解交给 AI。
- ❌ 不接线上代码仓（GitHub/GitLab 等）。
- ❌ 不做构建/单测/CI/静态扫描平台。
- ❌ 不做代码修改/自动修复。
- ❌ 不做函数级覆盖率（功能 E2E 不采集）。
- ❌ 不追求"全语言精确解析"；精度不足时以"文件级 + AI 理解 + 人工可调"兜底。

---

## 2. 设计原则

1. **AI-first**：代码理解由 AI Agent 完成（读文件/diff/上下文，产出结构化事实与证据）；系统不假设语言。
2. **确定性兜底**：凡是与语言无关、可确定计算的事实（commit、文件树、diff 文件列表、锚点、历史证据）一律确定性生成，保证可复现。
3. **事实 + 证据 + 置信度**：AI 事实必须带证据（file:line 片段 + 摘录哈希）与置信度；无证据不入库为"事实"。
4. **缓存与版本化**：按 `(repo, commit, file_hash, 模型, 提示词版本)` 缓存；AI 分析结果按版本保存，不静默覆盖。
5. **按需下钻**：默认给事实；需要细节时 `code_read` 读指定片段（限长、审计），不整仓塞上下文。
6. **可降级**：AI 不可用/预算受限 → 仅 T0 文件级（仍能做文件级影响候选 + 人工确认）。
7. **人工可调**：影响面/受影响用例可人工勾选/取消；`unmapped_changes` 显式提示。
8. **安全**：只读挂载/只读 git；按需读片段；代码不整仓外发；审计谁在何时读了什么。

---

## 3. 两层模型

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ T1 AI 代码理解层（核心，语言无关）                                           │
│  · Code Analyst Agent / 工具：读 diff 与相关文件 → 结构化代码事实 + 证据      │
│  · 产出：页面/路由/接口/组件/符号/依赖/变更语义；source=ai；confidence        │
│  · 缓存：commit + file_hash + 模型/提示词版本；可校验、可人工校正             │
├─────────────────────────────────────────────────────────────────────────────┤
│ T0 通用层（确定性、语言无关）                                                │
│  · Git 只读：commit/branch、文件树、文件元数据、`git diff`（文件级）          │
│  · 锚点：用例/需求/问题 ↔ 文件/位置（人工或证据产生）                        │
│  · 任何语言都成立；影响分析的保底输入                                        │
└─────────────────────────────────────────────────────────────────────────────┘
```

**降级顺序**：T1 不可用（AI 预算/故障）→ 仅 T0（文件级影响候选 + 人工确认），主流程不阻塞。

---

## 4. 概念与实体

```text
CodeRepository（本地 Git 路径/固定分支/排除规则）
   ├─< CodeIndexRun（一次索引/分析：commit、source t0/t1、模型/提示词版本、统计、错误）
   ├─< CodeEntity（kind: file/module/class/function/route/component/api；
   │              source: git/ai；confidence；evidence_refs）
   ├─< CodeEdge（contains/imports/defines/calls/renders/route_to_api；
   │             source: git/ai；confidence）
   └─< CodeChangeSet（base_commit → head_commit；changed files/entities）

CodeFact / CodeEvidence：
  AI 产出的结构化事实（路由/接口/页面/依赖）必须带 evidence（file:line + 片段摘录哈希）；
  无证据 → 只作为"线索"（低置信），不参与自动影响判定。

CodeAnchor：
  Requirement / Case / Issue / AppModel ↔ CodeEntity / 文件位置

API 契约（跨栈）：
  前端调用点（method + path，AI 识别）↔ 后端接口端点（AI 识别）
  ——语言无关：不假设前端/后端各是什么语言或框架
```

### 4.1 关键字段（概念）

| 实体 | 关键字段 |
|---|---|
| CodeRepository | id, name, local_path, fixed_branch, include/exclude globs, status, last_commit |
| CodeIndexRun | repo_id, commit, source(t0/t1), model/prompt_version, status, stats, errors |
| CodeEntity | id, repo_id, commit, kind, path, name, start/end line, source(git/ai), confidence, evidence_refs, meta |
| CodeEdge | repo_id, commit, src, rel, dst, source(git/ai), confidence |
| CodeChangeSet | repo_id, base_commit, head_commit, stats, changed_files, changed_entities(ai), created_at |
| CodeAnchor | repo_id, entity/file, subject_kind(requirement/case/issue/app_model), subject_id, confidence, created_by |
| CodeFactVersion | fact_key, version, model, prompt_version, commit, created_at, supersedes |

---

## 5. 代码理解流程

### 5.1 通用层（T0，始终可用）

```text
git 只读检查（仓库/分支/commit 可达）
  → 文件树扫描（include/exclude globs；排除 .git/node_modules/dist/vendor…）
  → 文件级元数据（path/size/mtime/hash）
  → git diff base..head → changed files
  → 锚点（已有的人工/证据锚点；文件级）
  → 写入 CodeEntity(kind=file, source=git) / CodeChangeSet
```

用途：文件级影响候选；大仓/AI 预算不足/未知语言时的保底。

### 5.2 AI 代码理解层（T1，核心）

触发时机（按需，避免全仓分析）：
- 版本创建/批次开始时：分析 **diff 变更文件**（优先）+ 受影响页面/接口相关文件；
- 用例设计时：按需求检索/读取相关代码（`code_search`/`code_read`）→ 产出设计所需事实；
- 影响分析时：读 diff + 候选文件 → 判断影响；
- 失败分析时：读堆栈/接口相关位置。

AI 产出（结构化，必须带证据）：
- 页面/路由（frontend route/page）、接口（API endpoint：method/path/入口位置）、组件、关键符号；
- 依赖/调用关系（能识别的）；
- 变更语义摘要（改了什么、影响哪个功能面）。

约束：
- 每次分析输入 = 变更文件 + 检索到的相关文件片段（限长）；不整仓喂入。
- 判断"某个路由/接口存在"必须基于代码证据（文件+行），不能仅凭常识。
- 结果写 `CodeEntity(source=ai, confidence, evidence_refs)`；低置信只作线索。
- 缓存：`(repo, commit, file_hash, model, prompt_version)`；同一输入复用，跨版本/批次共享。
- AI 分析失败/预算不足：降级 T0，并在 UI 标注"代码事实未分析/仅文件级"。

### 5.3 AI 事实校验（替代 parser 的确定性校验）

既然不设计 parser，AI 事实的可靠性靠以下机制保证：

1. **证据存在性校验**：每条事实必须引用 `path + start/end line + 摘录哈希`；入库前校验文件存在、行号范围合法、摘录哈希匹配（从工作树读取片段比对）。
2. **多次/分步提取**：关键事实（接口/路由）要求 AI 以"文件→实体→证据"的结构输出；不满足结构不入库。
3. **置信度分级**：`high`（证据完整且直读）/`medium`（证据存在但语义推断）/`low`（线索）；low 不参与自动影响判定。
4. **人工校正**：影响结果可勾选/取消；可标记"事实错误"，形成质量数据（CI-M1 可用于评测）。
5. **缓存与版本化**：同 commit 复用；模型/提示词升级产生新版本，旧版本保留可对比。
6. **质量指标**（CI-M1）：AI 事实命中率、人工修正率、影响分析漏报/误报率。

---

## 6. 代码事实与查询（Agent 工具，语言无关）

| 工具 | 作用 | 返回 |
|---|---|---|
| `code_repos` | 仓库/分支/索引状态 | 摘要 |
| `code_search` | 按关键字/路径/kind 检索代码实体（AI/git） | 摘要列表（含 confidence/source） |
| `code_read` | 读指定文件片段（限长、审计） | 片段 |
| `code_tree` | 文件树/目录结构（语言无关） | 文件列表 |
| `code_diff` | base..head 变更文件/实体/统计 | 摘要 |
| `code_analyze` | **触发/获取 AI 代码理解结果**（指定文件/范围） | 结构化事实 + 证据 + 置信度 |
| `code_routes` / `code_apis` | 路由/接口事实（AI） | 列表 + 证据 |
| `code_impact` | 变更影响：受影响用例/需求/页面/接口 + 原因/置信度 | 提案 |
| `code_anchors` | 某需求/用例/问题关联的代码位置 | 列表 |

规则：默认摘要；按需下钻；所有读取审计；不返回整仓内容；AI 事实可引用（带证据）。

---

## 7. 变更影响分析（核心）

### 7.1 输入

1. **T0 确定性**：`git diff` 文件列表、commit、已有文件级锚点。
2. **T1 AI 理解**：变更文件的路由/接口/页面/组件/依赖事实 + 变更语义。
3. **需求关系**：需求依赖/冲突/重叠/共享范围。
4. **锚点**：Case/Requirement/Issue ↔ AppModel/CodeEntity。
5. **历史**（可选）：近期失败/问题加权。

### 7.2 算法（语言无关）

```text
1) 变更文件集（T0）
2) 候选代码事实：diff 文件 → AI 分析（T1）→ 路由/接口/页面/组件变更
3) 映射 AppModel：页面/接口/组件 ↔ 代码事实（AI 映射 + 人工锚点）
4) 锚点反查：AppModel/CodeEntity → 用例、需求、历史问题
5) 需求关系扩展：新/变更需求 → 关联需求 → 关联用例（CI-M0 一跳）
6) AI 影响推理：读 diff + 候选用例/需求 → 输出受影响项 + 原因 + 置信度
7) 合并去重排序：直接命中 > 一跳到 > 需求关系扩展；低置信提示
8) 人工调整：前端"代码与变更"Tab 勾选/取消受影响用例
9) 未映射变更：无事实/无锚点的文件 → unmapped_changes（人工判断）
```

### 7.3 输出

```json
{
  "change_set_id": 42,
  "repo": "web-main",
  "base_commit": "abc1234",
  "head_commit": "def5678",
  "changed": { "files": 34, "routes": 5, "apis": 12, "components": 8,
               "sources": { "git": 34, "ai": 20 } },
  "affected": {
    "cases": [ { "case_id": 411, "reason": "changed api POST /api/refund",
                 "confidence": "high", "evidence": ["src/api/refund.ts:88"] } ],
    "requirements": [ 12, 13 ],
    "app_models": [ "route:/orders", "api:POST /api/refund" ]
  },
  "unmapped_changes": [ { "file": "src/utils/format.ts",
                          "reason": "no fact/anchor", "suggestion": "人工确认" } ]
}
```

### 7.4 可复现性

- 批次固定 `base_commit → head_commit`；影响分析输入固定。
- AI 事实按 `(commit, file_hash, model, prompt_version)` 缓存；同一 commit 重跑复用，结果稳定。
- AI 重分析/模型升级 → 新 fact 版本（不覆盖旧版本）；影响分析记录使用的 fact 版本。
- 报告记录 commit、fact 版本、影响面；同 commit 可解释"当时为什么选这些用例"。

---

## 8. 跨栈 API 契约映射（语言无关）

- 前端"调用点"与后端"接口端点"由 AI 识别，以 **method + 归一化 path** 匹配，建立 API 契约。
- 归一化：路径参数（`/{id}`）、前缀（网关/context-path）、大小写；匹配不上 → `unmapped_changes`。
- 影响链：前端调用/后端接口任一变更 → API 契约 → 页面/用例 → 受影响用例；跨栈传播。
- 不假设语言：前端不一定是 TS/JS，后端不一定是 Java；只依赖"调用点/端点"这类语义事实。

---

## 9. 与主流程集成

| 阶段 | 代码智能的作用 |
|---|---|
| 版本创建/澄清 | 选择本地仓库/固定分支/基线 commit；AI 可读代码补业务规则 |
| 用例设计 | AI 读需求相关代码/路由/接口 → 设计"基于代码现状"的用例；建立代码锚点 |
| 设计评审 | 检查"代码里有、脑图没覆盖"的入口/分支（AI 辅助） |
| 批次执行前 | 代码变更检测（head 变化）→ 影响分析 → 受影响用例 → 增量批次 |
| 执行/失败分析 | 按 URL/接口/堆栈定位代码位置；证据带 code_ref |
| 问题整理/报告 | 问题单含代码位置；报告含 commit/影响面/未映射变更 |
| 回归/增量 | 只跑新增 + 受影响用例；无代码变更时只跑需求关系影响 |

---

## 10. 性能、成本与安全

### 10.1 性能/成本

- 默认只分析 **diff 变更文件 + 相关检索文件**，不做全仓 AI 分析。
- 事实缓存跨批次/版本复用；AI 分析可异步、可限流、可预算。
- 大仓：排除目录、按需读取、分页；只分析相关文件。
- Token 度量：每用例 token、AI 代码分析 token、缓存命中率、下钻率。

### 10.2 安全

- 仓库只读挂载、只读 git；不执行代码、不联网、不改工作树。
- AI 只读指定片段；不整仓外发；审计谁/何时读了什么。
- 代码可能含密钥：不批量导出；报告/问题只放 code_ref 与必要摘要。
- 多用户共享仓库：普通用户只读查询，管理员配置。

### 10.3 边界

- 固定分支；版本/批次只记录 commit，不切分支、不改工作树。
- 工作树 dirty：以 commit 为准并提示；不自动处理。
- 执行期间代码变更（低频）：不自动打断；检测到 head 变化提示"代码有更新"，由用户决定是否开新批次。
- AI 事实存在不确定性：低置信不自动判影响；`unmapped_changes` 显式暴露；人工可调。

---

## 11. 代码智能里程碑（CI-M0/M1/M2）与验收

### 11.1 范围（语言无关）

| 里程碑 | 内容 |
|---|---|
| **CI-M0（实施 P4）** | T0（本地仓库配置/只读 git/文件树/文件级 diff/commit 基线/锚点）；T1（AI 代码理解：路由/接口/页面/组件事实 + 证据校验 + 缓存 + 置信度 + 降级）；影响分析（T0+T1+需求关系+锚点，输出提案可人工调整）；`code_*` 工具（含 `code_analyze`）；代码与变更 Tab；用例/问题 code_ref；AI 不可用降级 |
| CI-M1（P4/P5/P8 增强） | AI 辅助的依赖/调用关系与影响精度提升；增量索引；设计评审用"代码有、脑图没覆盖"；未提交改动提示；AI 事实质量评估（命中率/人工修正率）；影响分析误报/漏报度量 |
| CI-M2（P8 之后远期候选） | 字段级/跨栈深度分析；覆盖率联动；事实质量自动校准 |

> 实施阶段映射：CI-M0 = 实施 P4；CI-M1 的增强项按能力分别归入 P4/P5/P8；CI-M2 为远期候选，需另立决策（实施 P4 明确不做字段级分析）。

### 11.2 验收场景（语言无关）

1. 任意语言仓库只读挂载后，能拿到文件树、commit、diff 文件列表（T0 不依赖语言）。
2. 对一段未知语言的变更，AI 能读出"改了哪些接口/路由/页面/关键逻辑"并给出证据（T1）；无法识别时明确标注，不编造。
3. 修改某个接口/路由后，影响分析给出受影响用例候选与原因；低置信项可人工确认。
4. 未映射变更显式展示，不静默漏掉。
5. 同 commit 重跑影响分析，结果复用缓存且一致；AI 重分析产生新版本而非覆盖。
6. 用例/问题可显示并跳转 code_ref；证据可引用代码片段。
7. AI 不可用/预算耗尽时降级 T0（文件级影响 + 人工确认），主流程不阻塞。
8. 批次执行/证据/报告带 base/head commit 与 fact 版本，可复现。
9. AI 产出的接口/路由事实必须能通过"文件+行+摘录哈希"校验；不通过的降为线索。

---

## 12. 剩余待确认

1. **代码仓规模**（文件数/单仓大小）：决定 AI 分析限流/分批策略。
2. **monorepo 子项目根目录**：人工配置或自动识别（CI-M0 人工配置即可）。
3. **AI 代码分析的预算上限**（每次影响分析/设计读多少文件、token 上限）。
4. **代码事实的人工校正入口**是否需要（CI-M0 先通过"受影响用例勾选"间接校正，CI-M1 可加事实修正）。
5. **事实质量指标口径**（命中率/修正率/影响漏报率）如何采集（CI-M1）。

---

## 13. DDL 草案（PostgreSQL，语言无关）

```sql
CREATE TABLE IF NOT EXISTS code_repositories (
    id             BIGSERIAL PRIMARY KEY,
    key            TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    local_path     TEXT NOT NULL,
    fixed_branch   TEXT NOT NULL DEFAULT 'main',
    include_globs  TEXT[] NOT NULL DEFAULT '{}',
    exclude_globs  TEXT[] NOT NULL DEFAULT '{".git/**","node_modules/**","dist/**","build/**","vendor/**"}',
    status         TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    last_commit    TEXT,
    last_indexed_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS code_index_runs (
    id               BIGSERIAL PRIMARY KEY,
    repo_id          BIGINT NOT NULL REFERENCES code_repositories(id) ON DELETE CASCADE,
    commit_sha       TEXT NOT NULL,
    source           TEXT NOT NULL CHECK (source IN ('t0','t1')),
    model            TEXT,
    prompt_version   TEXT,
    status           TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','failed')),
    stats            JSONB NOT NULL DEFAULT '{}'::jsonb,
    errors           JSONB NOT NULL DEFAULT '[]'::jsonb,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS code_entities (
    id             BIGSERIAL PRIMARY KEY,
    repo_id        BIGINT NOT NULL REFERENCES code_repositories(id) ON DELETE CASCADE,
    commit_sha     TEXT NOT NULL,
    kind           TEXT NOT NULL,           -- file/module/class/function/route/component/api/...
    path           TEXT NOT NULL,
    name           TEXT,
    start_line     INTEGER,
    end_line       INTEGER,
    signature      TEXT,
    source         TEXT NOT NULL DEFAULT 'git' CHECK (source IN ('git','ai')),
    confidence     TEXT NOT NULL DEFAULT 'medium' CHECK (confidence IN ('low','medium','high')),
    evidence       JSONB NOT NULL DEFAULT '[]'::jsonb,  -- [{file,start,end,excerpt_hash}]
    meta           JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_code_entities_repo_commit ON code_entities(repo_id, commit_sha, kind);
CREATE INDEX IF NOT EXISTS idx_code_entities_path ON code_entities(repo_id, path);

CREATE TABLE IF NOT EXISTS code_edges (
    id         BIGSERIAL PRIMARY KEY,
    repo_id    BIGINT NOT NULL REFERENCES code_repositories(id) ON DELETE CASCADE,
    commit_sha TEXT NOT NULL,
    src_id     BIGINT REFERENCES code_entities(id) ON DELETE CASCADE,
    rel        TEXT NOT NULL,               -- contains/imports/defines/calls/renders/route_to_api
    dst_id     BIGINT REFERENCES code_entities(id) ON DELETE CASCADE,
    source     TEXT NOT NULL DEFAULT 'ai' CHECK (source IN ('git','ai')),
    confidence TEXT NOT NULL DEFAULT 'medium'
);

CREATE TABLE IF NOT EXISTS code_change_sets (
    id             BIGSERIAL PRIMARY KEY,
    repo_id        BIGINT NOT NULL REFERENCES code_repositories(id) ON DELETE CASCADE,
    base_commit    TEXT NOT NULL,
    head_commit    TEXT NOT NULL,
    stats          JSONB NOT NULL DEFAULT '{}'::jsonb,
    changed_files  JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS code_anchors (
    id           BIGSERIAL PRIMARY KEY,
    repo_id      BIGINT NOT NULL REFERENCES code_repositories(id) ON DELETE CASCADE,
    entity_id    BIGINT REFERENCES code_entities(id) ON DELETE SET NULL,
    file_path    TEXT,
    start_line   INTEGER,
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('version','requirement','case','issue','app_model')),
    subject_id   BIGINT NOT NULL,
    confidence   TEXT NOT NULL DEFAULT 'medium',
    created_by   TEXT NOT NULL DEFAULT 'ai',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_code_anchors_subject ON code_anchors(subject_kind, subject_id);

CREATE TABLE IF NOT EXISTS code_fact_versions (
    id             BIGSERIAL PRIMARY KEY,
    repo_id        BIGINT NOT NULL REFERENCES code_repositories(id) ON DELETE CASCADE,
    commit_sha     TEXT NOT NULL,
    fact_key       TEXT NOT NULL,          -- 例如 route:/orders | api:POST /api/refund
    version        INTEGER NOT NULL,
    model          TEXT,
    prompt_version TEXT,
    body           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    superseded_at  TIMESTAMPTZ,
    UNIQUE (repo_id, commit_sha, fact_key, version)
);
```

---

*文档版本：v0.4（2026-10-26）：**不设计 parser**；语言无关；T0 通用 Git 层 + T1 AI 代码理解；事实必须带证据并通过存在性校验；影响分析带置信度、可人工调整、可复现。*
