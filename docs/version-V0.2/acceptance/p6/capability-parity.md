# P6 Capability Parity Matrix — 0.1 P0–P8 → 0.2

> P6 阶段验收产物；依据 `research/capability-matrix.md` §3 与 P0–P6 handoff/证据。
> 状态：`PASS` = 0.2 入口 + 验收证据；`P7` = P9 能力，P6 不实现；`external` = 依赖 0.1/P5 证据。

| # | 能力 | 0.2 入口 | P6 状态 / 证据 |
|---|---|---|---|
| A1 | 身份/权限 | `/api/auth/*`、`/api/admin/users/*`；登录/设置 | PASS（P0；P6 验收管理员+普通用户权限） |
| A2 | LLM 配置/网关 | `/api/admin/llm/*`、Agent runtime | PASS（P0；P6 真实 DeepSeek config test=200） |
| A3 | 事件/SSE | `/api/events`、`/api/stream` | PASS（P0–P5；链路/前端订阅） |
| A4 | 文件/证据 | `/api/content/*`、evidence | PASS（P0/P3/P5） |
| A5 | 审计/幂等/限流 | `/api/admin/audit-logs`；audit_log | PASS（P0；P6 prompt/eval 审计） |
| A6 | 配置/迁移/部署 | 设置中心、迁移 #1–#33 | PASS（P6 33 迁移/72 设置；A6 硬化归 P8） |
| B1 | 资源/应用模型 | `/api/resources/*` | PASS（P0/P2） |
| B2 | 账号/Session | 资源账号、执行上下文 | PASS（P0/P3） |
| B3 | 只读 DB 断言 | execution DB 断言 | PASS（P3） |
| B4 | 代码智能 | `/api/code/*`、代码与变更 Tab | PASS（P5 13/13） |
| C1 | 版本生命周期 | `/api/versions/*` | PASS（P1/P4） |
| C2 | 需求/澄清 | 需求与澄清 Tab、`/requirements/*` | PASS（P1） |
| C3 | 知识库（P9） | `/knowledge` 占位 | P7（D63；P6 Out） |
| D1 | 用例脑图 | 用例设计 Tab、`/design/*` | PASS（P2） |
| D2 | 设计生成/评审 | 设计团队/对话 | PASS（P2；无评审页签） |
| D3 | 视觉断言 | `view=visual`、`/api/quality/visual-metrics` | PASS（P5 13/13） |
| D4 | 脑图导出 | `exports` API | PASS（P2） |
| E1 | 波次/调度/并发 | `/waves/*` | PASS（P3） |
| E2 | 执行/证据 | 执行 Tab、case_run/evidence | PASS（P3） |
| E3 | 暂停/恢复 | 执行控制 | PASS（P3） |
| E4 | 失败分析/补全 | failure_analyst/coverage_analyst | PASS（P3/P4） |
| E5 | 证据 GC（P9） | `/api/admin/gc` 占位/未启用 | P7（D65） |
| F1 | 问题 | 问题 Tab、`/issues` 全局中心 | PASS（P4 + P6 全局只读） |
| F2 | 报告 | 报告 Tab、`/reports` 全局中心 | PASS（P4 + P6 全局只读；PDF 归 P7） |
| F3 | 全局问题/报告中心 | `/issues`、`/reports`、`/api/issues`、`/api/reports` | **P6 PASS**（跨版本只读/深链/游标） |
| G1 | Agent run/message | `/api/agent-center/runs`、agent_run | PASS（P1 + P6 只读观察） |
| G2 | Agent 中心/评测 | `/agents`、`/api/agent-center/*`、`/api/quality/*` | **P6 PASS**（目录/运行/质量/评测/提示词配置） |
| G3 | 任务板/频道/上下文 | work_item/team_room/context_packet | PASS（P1） |
| G4 | LLM 路由/预算 | settings/profile budgets | PASS（P0/P1；成本看板后置） |
| H1 | 应用壳/版本详情 | 8 项全局壳 + 8 Tab | PASS（P0–P5） |
| H2 | 澄清 UI | 需求与澄清 Tab | PASS（P1） |
| H3 | 脑图 UI | 用例设计 Tab | PASS（P2） |
| H4 | 执行 UI | 执行 Tab | PASS（P3/P5） |
| H5 | 问题/报告 UI | 问题/报告 Tab | PASS（P4） |
| H6 | 搜索/待办/链路 | CommandPalette、TodoDrawer、链路 Tab | **P6 PASS**（5 类搜索/6 kind 待办/链路 v2） |
| I1 | 测试/fake/LLM 验收 | 单测/集成/E2E、真实 LLM 验收 | PASS（P6 真实 LLM+Chrome；无 fake 验收） |
| I2 | 打包/CI/SLO/安全/备份 | 部署/验收脚本 | PASS（P5/P6 局部；完整多实例/CI 归 P8） |

## P6 新增入口汇总

- 质量/评测：`/quality`、`/api/quality/{overview,eval-runs,visual-metrics}`。
- 搜索：Topbar CommandPalette、`GET /api/search`。
- 待办：Topbar TodoDrawer、`GET /api/todos`。
- Agent 中心：`/agents`、`/api/agent-center/{catalog,runs,quality}`、`/api/agents/profiles*`、`/api/agents/tools`。
- 全局中心：`/issues`、`/reports`、`GET /api/issues`、`GET /api/reports`。
- 链路 v2：`/versions/{id}?tab=chain`、`GET /api/versions/{id}/exploration-chain` 增量。

> P7/P9 行（C3/E5/F2 PDF/G2 评测扩展等）不在 P6 实现；P6 只保留入口/占位/只读。
