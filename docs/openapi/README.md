# `docs/openapi/api.yaml` — EzTest 0.2.1 base + v0.22 additive contract

本目录存放**对外发布**的 OpenAPI 3.0.3 快照（0.2.1 基线由 Task 85 整体 port，
v0.22 additive 策略见下）。身份分两层：

- **0.2.1 基线身份**：`info.version: 0.2.1`、`x-frozen: 0.2.1`；权威基线为
  **181 paths / 222 operations / 281 schemas**（events 目录口径：131 events）。
- **v0.22 增量（additive）**：Task 37 在基线上追加 W0 契约（23 个 path item /
  28 个 operation / 34 个 schema），当前文件为 **204 paths / 250 operations /
  315 schemas**。新增段在 `info.description` 的
  `### v0.22 W0 增量（Task 37，additive）` 中说明。
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
2. v0.22 W0 只允许**追加**新的 path item、operation、schema 和可选字段；
   追加内容集中在 `docs/openapi/api.yaml` 的 W0 区块，并在本 README 记录。
3. v0.22 增量本身由 `scripts/v022-contract-check.cjs` 断言：W0 的 23 个 path item /
   28 个 operation 与关键 schema 必须存在，且文件仍保留 0.2.1 基线身份。
4. 基线计数 `181/222/281` 保留为本 README/历史的**基线口径**；当前文件实际计数
   允许为 `181+extras / 222+extras / 281+extras`，两个 gate 各自报告 extras。

当前 v0.22 W0 追加的契约面（详见 `api.yaml` 的 W0 段）：

- 计划写入、handoff 读取、事实仲裁、需求抽取、附件图片、绑定/多仓/版本账号；
- 结构化提问（Host 创建/重开、用户 answer/skip）、阶段写入 request/advance/block/unblock/
  pause/resume，以及经 StageService 审计回边的硬门 reject；
- 对应 DTO/schema 与 `kind`/`revision` 等可选字段。

## 验证

```bash
node scripts/v021-contract-check.cjs   # 0.2.1 基线子集（无缺失；extras 允许并报告）
node scripts/v022-contract-check.cjs   # v0.22 W0 additive 路径/operation/schema
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
