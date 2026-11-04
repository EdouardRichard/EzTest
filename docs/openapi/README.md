# `docs/openapi/api.yaml` — EzTest 0.2.1 release contract

本目录存放**对外发布**的 OpenAPI 3.0.3 快照。当前 release 身份：

- `info.version: 0.2.1`
- `x-frozen: 0.2.1`
- **181 paths / 222 operations / 281 schemas**（events 目录口径：131 events）
- 公开端点 `GET /api/openapi.yaml` 原样返回本文件

## 单一事实源与整文件 port

权威契约是 [`docs/version-V0.2/contracts/openapi.yaml`](../version-V0.2/contracts/openapi.yaml)
（0.2.1 increment 亦见 [`contracts/README.md`](../version-V0.2/contracts/README.md) 的 0.2.1 状态行）。
本文件由 **Task 85（Release-T1）** 对该权威契约执行**整体 port** 生成，不是对
`0.2.0-p8` 快照的局部拼接：逐条路由/字段打补丁会把快照半更新并破坏 P8 身份，因此禁止。

整文件 port 后只允许修改 release 身份与说明文字：

1. `info.title` / `info.version` → 0.2.1；
2. `info.description` 的 0.2.1 段标记与 release provenance；
3. 顶层 `x-frozen` → 0.2.1；
4. 本 README。

`servers` / `security` / `tags` / `paths` / `components` 必须与权威契约逐字节一致，
由 `scripts/v021-contract-check.cjs` 的 release gate 断言。P0–P8 历史段落与 P8 冻结
快照身份（P8 历史计数 **162 paths / 200 operations / 264 schemas**）保留在
`info.description` 中，不被 0.2.1 改写。

> 权威契约文件自身按 Task 84 的裁定保留文件级 P8 身份（`info.version: 0.2.0-p8`、
> `x-frozen: P8`、增量段标 `draft: 0.2.1`）；本文件是 0.2.1 release 的公开快照，
> 两者 paths/operations/schemas 完全一致。

## 0.2.1 release 内容（整体 port 自权威契约）

相对 `0.2.0-p8` 快照，本次 port 带入 0.2.1 的全部契约增量：

- **22 个 operation / 19 个新 path item**：公开模型列表、会话模型覆盖、消息 parts、
  版本阶段/两硬门/退回、work_item/plan/facts 查询、知识结构化检索、需求草案清理
  （`POST .../requirements/dedupe`、`POST .../requirements/merge`、
  `DELETE .../requirements/{rid}`）、作业控制 canonical 路由与版本级别名、
  admin-only context packet、模型能力 PATCH；
- **17 个新 schema**（`WorkItemView`、`PlanItemView`、`FactView`、`MessagePartView`、
  `JobView`、`RequirementDedupeResult` 等）与 15 个既有 schema 的 0.2.1 可选字段；
- **`job_card` 的 `code`/`reason`**（仅终态 failed，可选，≤64/≤2000）、`cancelable`、
  conversation/LLM 能力字段、`execution_verdict`、知识 `bindings`/`last_verified_at`；
- 阶段、作业控制、需求清理错误码与说明（409 `state_conflict`、
  `invalid_stage_for_gate` 等）。

未挂载/预留项沿用权威契约的说明：4 个读路由（work-items/plan-items/facts）为
Task 62 契约先行、`GET /api/versions/{id}/jobs/{jobId}` 为 0.3 预留并标
`deprecated`、context packet 读取待 Task 48 装配。本次 port **不改变任何后端/前端行为**，
只更新发布文档。

## 验证

```bash
node scripts/v021-contract-check.cjs
```

release gate（Task 85 扩展，不弱化 frozen 检查）会断言：release 身份为 0.2.1；
181/222/281 计数；22 个 0.2.1 operation 与 17 个 schema 存在；release 的
paths/operations/schemas 与权威契约完全一致（无缺失/多余）；`servers` 之后正文逐字节
一致；P1–P8 历史段落与 P8 历史计数保留；本 README 记录整文件 port 规则。

YAML 解析与本地 `$ref` 自检（使用 `frontend` 的 `yaml` 依赖）：

```bash
node -e "const fs=require('fs'),Y=require('./frontend/node_modules/yaml');const d=Y.parse(fs.readFileSync('docs/openapi/api.yaml','utf8'));console.log(Object.keys(d.paths).length, Object.keys(d.components.schemas).length)"
```

## 历史

- `0.2.0-p8`：上一份公开快照（162 paths / 200 operations / 264 schemas），由 P8
  硬化阶段冻结；其身份说明保留在本文件 `info.description` 中。
- `0.2.1`：本文件；整体 port 自 0.2.1 权威契约（Task 85，2026-11-04）。
