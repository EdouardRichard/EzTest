# `docs/openapi/api.yaml` — EzTest 0.2.1 base + v0.22 additive contract

本目录存放**对外发布**的 OpenAPI 3.0.3 快照（0.2.1 基线由 Task 85 整体 port，
v0.22 additive 策略见下）。身份分两层：

- **0.2.1 基线身份**：`info.version: 0.2.1`、`x-frozen: 0.2.1`；权威基线为
  **181 paths / 222 operations / 281 schemas**（events 目录口径：131 events）。
- **v0.22 增量（additive）**：Task 37 在基线上追加 W0 契约（23 个 path item /
  29 个 operation / 34 个 schema），W1 继续 additive 追加（W1-7/D1 的波次启动
  operation、W1-8/D8 的执行器认证检查 path/operation、W1-13/API-19 的
  `GET /api/versions/{id}/scope-snapshot`，以及 W1-14A/API-36/37/39/40 的
  discovery observations/deltas/coverage 与 gaps 读，合计 **29 个 path item /
  35 个 operation / 34 个 schema**）。当前文件为 **210 paths / 257 operations /
  315 schemas**。新增段在 `info.description` 的
  `### v0.22 W0 增量（Task 37，additive）`、`## v0.22 W1 增量（W1-13，additive）`
  与 `## v0.22 W1 增量（W1-14A，additive）` 中说明。
- 公开端点 `GET /api/openapi.yaml` 原样返回本文件。

## 单一事实源与整文件 port（0.2.1 基线）

权威 0.2.1 契约是 [`docs/version-V0.2/contracts/openapi.yaml`](../version-V0.2/contracts/openapi.yaml)
（0.2.1 increment 亦见 [`contracts/README.md`](../version-V0.2/contracts/README.md) 的 0.2.1 状态行）。
本文件的 0.2.1 基线由 **Task 85（Release-T1）** 对该权威契约执行**整体 port** 生成，
不是对 `0.2.0-p8` 快照的局部拼接：逐条路由/字段打补丁会把快照半更新并破坏 P8 身份，因此禁止。
P0–P8 历史段落与 P8 冻结快照身份（P8 历史计数
**162 paths / 200 operations / 264 schemas**）保留在 `info.description` 中，不被改写。

## v0.22 增量策略（additive）

从 Task 37 起，本文件不再要求与 0.2.1 权威契约逐字节一致，改为**基线子集 + additive 扩展**：

1. 0.2.1 基线的每一条 path / method / schema **必须仍然存在且语义不变**；
   不允许删除、重命名或改写 0.2.1 条目（`scripts/v021-contract-check.cjs` 断言“无缺失”）。
2. v0.22 additive 增量只允许**追加**新的 path item、operation、schema 和可选字段；
   追加内容可新增 path item，也可在既有 path item 下新增 operation
   （如 W1-7/D1 的 `POST /api/versions/{id}/waves`），并在本 README 记录。
3. v0.22 增量本身由 `scripts/v022-contract-check.cjs` 断言：W0 的 23 个 path item /
   29 个 operation 与关键 schema 必须存在，另有 W1 的新 path/operation（W1-7 复用
   既有 `waves` path item 的 operation；W1-8、W1-13、W1-14A 合计新增 path 与
   operation），且文件仍保留 0.2.1 基线身份。
4. 基线计数 `181/222/281` 保留为本 README/历史的**基线口径**；当前文件实际计数
   允许为 `181+extras / 222+extras / 281+extras`，两个 gate 各自报告 extras。

当前 v0.22 追加的契约面（详见 `api.yaml` 的 W0/W1 段）：

- 计划写入、handoff 读取、事实仲裁、需求抽取、附件图片、绑定/多仓/版本账号；
- 结构化提问（Host 创建/重开、用户 answer/skip）、阶段写入 request/advance/block/unblock/
  pause/resume，以及经 StageService 审计回边的硬门 reject；
- 波次启动 `POST /api/versions/{id}/waves`（W1-7/D1，复用既有 GET path item 的 path）；
- 执行器认证检查 `POST /api/admin/executors/{id}/check`（W1-8/D8，admin；返回
  `auth_status: ok|unauthenticated|unreachable`，区分离线与 Bearer token 认证失败）；
- 波次冻结范围快照 `GET /api/versions/{id}/scope-snapshot?wave_id=`（W1-13/API-19，
  inline schema；`repository_ids` 为多仓 id 集合；返回 `{wave_id,status,scope_snapshot,counts}`，
  当前 wave 冻结、新 approved 资源只在下一 wave 生效）；
- 执行期探索只读面（W1-14A/API-36/37/39/40，inline schema）：
  `GET /api/versions/{id}/discovery/observations`（DB-29 观测，ID 为十进制字符串，
  `limit` 默认 50、数值越界 clamp 到 1..200，含 source_kind/created_at/updated_at）、
  `GET .../discovery/deltas`（五类计数 + 精简来源）、`GET .../coverage`（发现侧覆盖矩阵：
  observed 按模板去重，uncovered 为 proposed 资源身份（API 为 `METHOD template`、page
  为 template），去重/升序/最多 200；case 覆盖 T14 属 W2）、`GET .../gaps`
  （current kind=gap 事实，共用 httpx.List 信封 data/next_cursor/has_more，空数组而非
  404，100 行上限时 has_more=true）；
- 对应 DTO/schema 与 `kind`/`revision` 等可选字段。

## 验证

```bash
node scripts/v021-contract-check.cjs   # 0.2.1 基线子集（无缺失；extras 允许并报告）
node scripts/v022-contract-check.cjs   # v0.22 additive 路径/operation/schema
```

0.2.1 release gate（Task 85 扩展，不弱化 frozen 检查）断言：0.2.1 基线身份仍为
0.2.1；`181/222/281` 基线计数只作为基线口径；22 个 0.2.1 operation 与 17 个
schema 存在；0.2.1 的 paths/operations/schemas 是当前文件的**子集**（无缺失/重命名，
v0.22 extras 允许并打印）。未挂载/预留项：4 个读路由（work-items/plan-items/facts）
为 Task 62 契约先行、Task 37 已由 P6 read model 挂载；`GET /api/versions/{id}/jobs/{jobId}`
为 0.3 预留并标 `deprecated`；context packet 读取已在 Task 48 装配。`servers` 之后的
0.2.1 语义保留；P1–P8 历史段落与 P8 历史计数保留；本 README 记录 additive 策略。

YAML 解析与本地 `$ref` 自检（使用 `frontend` 的 `yaml` 依赖）：

```bash
node -e "const fs=require('fs'),Y=require('./frontend/node_modules/yaml');const d=Y.parse(fs.readFileSync('docs/openapi/api.yaml','utf8'));console.log(Object.keys(d.paths).length, Object.keys(d.components.schemas).length)"
```

## 历史

- `0.2.0-p8`：上一份公开快照（162 paths / 200 operations / 264 schemas），由 P8
  硬化阶段冻结；其身份说明保留在本文件 `info.description` 中。
- `0.2.1`：整体 port 自 0.2.1 权威契约（Task 85，2026-11-04）。
- `v0.22 W0`：在 0.2.1 基线上 additive 追加 W0 契约（Task 37，2026-11-05）。
- `v0.22 W1`：追加波次启动（W1-7）、执行器认证检查（W1-8）与波次冻结范围快照
  （W1-13/API-19，2026-11-05）；W1-14A 追加探索观测/增量/覆盖与 gap 只读 API
  （2026-11-05）。
