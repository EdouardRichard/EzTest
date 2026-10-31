# EzTest 0.2 阶段间契约

> 状态：模板/机制说明；具体内容由各阶段实施时写入并冻结。
> 规则：**代码先行、契约后补**不允许；阶段末必须冻结本阶段对外接口。
>
> **P0 冻结状态（2026-10-31）**：`openapi.yaml`（43 paths / 66 operations，`x-frozen: P0`）、`events.md`、`db-schema.md`、`migrations-order.md`、`settings.md` 已冻结。`task-types.md` 与 `agent-profiles-tools.md` 按机制说明从 P1 开始写入；P0 未创建。

## 目录内容

| 文件 | 内容 | 首次写入 | 后续维护 |
|---|---|---|---|
| `openapi.yaml` | REST/SSE 对外契约 | P0 骨架 | 每阶段增量 |
| `events.md` | 事件类型/envelope/作用域字段 | P0 | 每阶段增量 |
| `db-schema.md` | 表、关键字段、约束、索引 | P0 | 每阶段增量 |
| `migrations-order.md` | 迁移文件顺序与 ownership | P0 | 每阶段追加 |
| `task-types.md` | work_item 类型/状态/去重键 | P1 | 每阶段增量 |
| `agent-profiles-tools.md` | profile、工具白名单、作用域注入、提交 schema | P1 | 每阶段增量 |
| `settings.md` | env 4 项、DB 设置键/默认/热加载/重启标记 | P0 | 每阶段增量 |
| `handoff-template.md` | 阶段 handoff 模板 | 已有 | 按需 |

## 冻结规则

1. 阶段内可以改契约；阶段末把本阶段对外接口写入并标记 `frozen: <phase-id>`。
2. 下一阶段只消费 frozen 契约；需要破坏性变更时，先停止、说明影响面、更新本目录 + 决策记录，再继续。
3. 契约变更必须同步：调用方/测试/迁移/handoff；不允许只改代码。
4. 共享契约文件由阶段 Lead 统一收口；子代理不并发写同一个契约文件。
5. 数据库迁移只追加；已冻结迁移不得修改；顺序以 `migrations-order.md` 为准。

## 最小字段约定

- **API**：method/path/request/response/error codes/idempotency。
- **事件**：type/channel/scope ids/payload version/是否持久化。
- **表**：表名/主键/关键字段/唯一约束/索引/所属上下文/迁移文件。
- **任务**：type/team/input scope/dedupe key/输出/失败语义。
- **工具**：name/scope 注入字段/参数 schema/提交校验/权限。
- **设置**：key/type/default/secret/restart_required/热加载方式。

## 交接检查

阶段末 handoff 必须回答：

1. 本阶段改了哪些契约？是否 frozen？
2. 下一阶段可以依赖哪些接口/表/事件/任务/设置？
3. 哪些是临时/占位，下一阶段必须替换？
4. 迁移顺序、回滚/备份影响？
5. 测试命令与验收证据路径？
