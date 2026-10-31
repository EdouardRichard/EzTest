# EzTest 0.2 参数决策（已关闭）

> 状态：**已关闭**（2026-10-31，用户确认全部按推荐）。
> 说明：这些是参数/边界级决策，不改变 0.2 主架构；以下为已定值，作为架构/概要设计的补充。
> 关联：[决策日志](decision-log.md) · [架构设计](../architecture-design.md) · [概要设计](../high-level-design.md)

## 决策摘要

| # | 参数 | 已定值 |
|---|---|---|
| OD-1 | 用例导出 | Markdown / Mermaid / OPML / CSV；Excel 后置 |
| OD-2 | 报告 | HTML 权威 + 独立 headless 渲染 worker 出 PDF；1 套内置模板 + 章节开关；版本化；不做自定义模板编辑器 |
| OD-3 | 知识 scope | `global` + `system`；显式晋升；管理员审核；标签 + PG 全文；不做向量 RAG |
| OD-4 | GC | 统一 content ledger；dry-run 默认；24h 宽限；临时 24h / 被替代导出 7d / 无引用 30d；旧 report revision 不自动删 |
| OD-5 | 0.1 数据 | 新库；不原地迁移；旧库只读归档/导出；可选一次性导出脚本 |
| OD-6 | 账号解析 | 用例只指定 `account_role`；执行按 `version_sut + role` 解析；同 SUT 多角色可并存 |
| OD-7 | 页面/API URL | 同时存完整 URL 与 route_template / method+path_template；管理员确认 AI 提议；去重按 template；页面/API 变更不自动全部 stale |
| OD-8 | 多 SUT 并发 | 全局并发 + 每 `version_sut` 配额；跨系统用例默认低并发（1）；上下文 TTL + 波次结束清理 |
| OD-9 | 团队频道摘要 | 每 20 条或 10 分钟生成；保留最近 N 版摘要；帖子全量审计；只读相关帖子进上下文；不做自由群聊控制面 |
| OD-10 | 需求字段 | 必填 title/description/acceptance/priority；推荐 target_version_suts/pages/apis、业务规则、前置、异常、数据约束、依赖；AI 草案 + 用户确认 |
| OD-11 | 报告种类 | 每波次 `report.wave` + 版本 `report.version`；版本总结在收尾前生成；两者均可 HTML/PDF；旧 revision 不覆盖 |

---

## OD-1 用例导出格式【已定：B】

**为什么重要**：设计页已有“导出用例”入口；导出物要进入 `export` 实体与 GC 账本，格式影响模板/依赖/验收。

**选项**
- A. Markdown + Mermaid + OPML（0.1 已有，最小兼容）
- B. A + CSV（表格工具消费）
- C. A + CSV + Excel（.xlsx）
- D. 只做 JSON（API 消费，不面向人）

**推荐默认：B（Markdown/Mermaid/OPML/CSV）**。Excel 需要额外依赖，放后续；JSON 可内部提供但不算“导出”。

**影响**：`export.format` 枚举、导出 API、GC 引用、验收样例；CSV 需要列定义（需求/场景/用例/步骤/断言/SUT/页面/API/优先级/状态）。

**需要拍板**：是否要 Excel？CSV 列模板是否按“一行一步骤”还是“一行一用例”。

---

## OD-2 P9 报告参数【已定：推荐默认】

**为什么重要**：D59 是 0.2 明确交付项；渲染 worker、模板、PDF 生成方式影响部署与运维。

**推荐默认**
- HTML 为权威产物；PDF 由独立 headless Chromium render worker 从冻结 HTML 生成（复用 Playwright 镜像但独立进程）。
- 内置 1 套模板 + 章节开关（摘要/用例结果/问题/覆盖/证据计数/附录）；模板版本化；0.2 不做用户自定义模板编辑器。
- 波次报告 + 版本总结；`report_revision` append-only；PDF 可重生成；旧 revision 不覆盖。
- 证据以缩略图/链接/计数呈现，不把大图嵌入 HTML。

**需要拍板**：PDF 是否必须服务端生成（还是前端打印即可）？模板是否要 2 套（对外精简/对内详细）？是否接受新增 render worker 进程？

**影响**：`report_render_job`/`render_status`、部署服务数、字体/分页、GC 对 PDF 的保护。

---

## OD-3 跨版本知识 scope【已定：global+system】

**为什么重要**：D63 是 0.2 交付项；知识是系统级，但“系统级”可能是全库或按 SUT 族。

**推荐默认**
- `scope=global`（全库）与 `scope=system`（同一 SUT 族）两种；版本只是来源。
- 版本知识显式晋升；管理员审核；`promoted_from_version_id`；支持撤回/复审。
- 注入澄清/设计；标签+PostgreSQL 全文检索；预算限制；不做向量 RAG。
- 冲突/过时：保留来源引用，知识标记 stale/revoked，注入时排除。

**需要拍板**：是否需要 `system` scope，还是所有知识都 global？谁有审核权（仅 admin / 系统 owner）？是否允许普通用户直接创建 global 知识？

**影响**：`knowledge_item` scope/审核字段、权限、注入过滤、管理页。

---

## OD-4 GC 保留期与对象范围【已定：推荐保留期】

**为什么重要**：D65 要覆盖附件/导出/报告/孤儿；误删不可逆。

**推荐默认**
- 统一 `content_ref` 账本；默认 dry-run；宽限期 24h；审计。
- 保护：被版本/需求/用例/问题/报告/知识/视觉基线引用的对象。
- 只清无引用/临时/被替代导出/旧 PDF；旧报告 revision 与有引用附件不自动删。
- 保留期默认：临时文件 24h；被替代导出 7d；无引用内容 30d；可配置。
- 删除留 tombstone/审计，不做物理立即删除。

**需要拍板**：旧报告 revision 是否永远不删？附件在版本删除/归档后是否可清？保留期是否用这些默认值？

**影响**：`content_ref`、`export/report_revision` 引用保护、GC API/UI/调度、备份策略。

---

## OD-5 0.1 数据迁移【已定：新库】

**为什么重要**：0.2 全量重写、新 schema；用户可能有 0.1 预览数据。

**推荐默认**
- 不做原地迁移；0.1 库只读归档/导出；0.2 新库。
- 提供一次性导出脚本（版本/需求/用例/问题/报告元数据）作为参考，不保证可回灌。
- 若用户要求平滑升级，需单独立项（迁移映射+验证），不塞进 0.2 主链。

**需要拍板**：是否接受“旧库只读、0.2 新库”？是否需要导出脚本作为交付物？

**影响**：迁移工具、部署文档、验收、数据模型兼容性。

---

## OD-6 跨系统用例账号解析【已定：角色解析】

**为什么重要**：一版本多 SUT；同一 SUT 可能多角色账号；跨系统用例需要多个账号。

**推荐默认**
- 用例/步骤只指定 `account_role`；执行时按 `version_sut + role` 从版本绑定解析具体账号。
- 同 SUT 多角色可并存；`case_run_sut_context` 每个 `(version_sut, role)` 一个上下文。
- 设计时只看角色，不暴露密码；执行时通过 `account.credentials` 取用。

**需要拍板**：是否允许设计时指定具体账号（而非角色）？角色命名是否自由文本还是枚举？

**影响**：`test_account.roles`、`case_step.account_role`、执行器上下文、凭据工具。

---

## OD-7 页面/API URL 粒度与发现去重【已定：url+template，管理员确认】

**为什么重要**：SUT 有多条页面链接与 API；设计/执行需要稳定目标；AI 探索会提出新页面/API。

**推荐默认**
- 页面同时存 `url`（完整）与 `route_template`（如 `/order/{id}`）；API 存 `method + path_template`。
- 页面/API 归属 `sut + environment`；版本绑定选 scope。
- 管理员可新增/导入/编辑；AI 探索只提议，管理员/用户确认后 `approved`。
- 去重键：`sut + environment + route_template/method+path_template`；重复提议合并。
- 页面/API 清单变更：已确认用例是否 stale 由设计团队评估；不自动全部失效。

**需要拍板**：URL 是否只存 route_template（更稳定）？页面发现是否允许 AI 自动入库（当前推荐需确认）？页面/API 变更是否自动 stale 用例？

**影响**：`sut_page/sut_api`、版本 scope、设计用例目标、执行导航、覆盖统计。

---

## OD-8 多 SUT 执行器并发/配额/清理【已定：默认低并发】

**为什么重要**：跨系统用例要同时占用多个 SUT 的浏览器上下文；资源争用/死锁/清理不当会导致执行不稳定。

**推荐默认**
- 同版本单活跃波；全局并发 + 每 `version_sut` 并发配额。
- 跨系统用例一次性预留所需 SUT 配额；默认低并发（如 1）避免 A 等 B 死锁。
- 单 SUT 用例可并行；跨系统用例按步骤顺序执行。
- 执行器上下文 TTL + 波次结束清理；实例崩溃按 lease/epoch 回收。
- 失败/超时先 cleanup，再释放配额。

**需要拍板**：每 SUT 默认并发数？跨系统用例是否默认串行？执行器上下文 TTL 默认值？

**影响**：scheduler、executor、证据清理、SLO。

---

## OD-9 团队频道摘要生成/保留/成本【已定：20 条/10 分钟，最近 N 版】

**为什么重要**：频道是协作/观察层；摘要进入上下文，影响 token 成本与 Agent 质量。

**推荐默认**
- 每 `版本×团队` 一个频道；结构化帖子；planner 读摘要，worker 读相关帖子。
- 摘要按事件/时间窗口生成（如每 20 条或 10 分钟），保留最近摘要 + 钉住决定/阻塞。
- 帖子保留全量（审计），摘要只保留最近 N 版；注入按相关度/标签过滤。
- 不在频道里做自由聊天触发；人类 `@` 只生成 work item。

**需要拍板**：摘要生成频率？保留多少版？是否允许 Agent 在频道里发自由文本（当前只允许结构化帖子）？

**影响**：`team_room_post/digest`、上下文包、token 成本、UI 频道。

---

## OD-10 需求字段模板【已定：推荐字段】

**为什么重要**：AI 从对话/附件“按系统要求录入需求”，字段决定澄清质量、设计输入与报告覆盖。

**推荐默认**
- 必填：标题、描述、验收标准、优先级。
- 推荐结构化：目标 SUT（可空，设计推断）、目标页面/API（可空）、业务规则/约束、前置条件、异常与边界、数据约束、依赖/冲突。
- 允许 AI 先填草案；用户编辑/确认时校验必填；空字段不阻塞草案，但确认前提示。
- 字段长度/格式限制由 schema 定义；附件作为来源引用，不复制全文。

**需要拍板**：业务规则/前置/异常是否必须为必填？目标 SUT/页面/API 是否必须显式选择，还是允许设计团队推断？

**影响**：`requirement_version` 字段、澄清/设计 prompt、确认校验、UI 编辑器、覆盖矩阵。

---

## OD-11 版本总结报告 vs 波次报告【已定：波次+版本总结】

**为什么重要**：D59 报告要明确“每个波次一份”还是“版本一份”，影响 revision/as-of/导出/GC。

**推荐默认**
- 每个波次一份 `report(kind=wave)`；版本收尾时生成 `report(kind=version)` 汇总。
- 版本总结包含：需求闭环、跨波次结果汇总、问题状态、覆盖、SUT 分组、波次差异。
- 两者都 revision/HTML/PDF；版本总结可手动重生成；旧 revision 不覆盖。
- 报告页默认展示版本总结；可切波次报告。

**需要拍板**：版本总结是否只在 completed 时生成？是否允许 running 中手动生成？波次报告是否也需要 PDF？

**影响**：`report.kind`、报告页、导出/GC、验收。

---

## 关闭记录

- 2026-10-31：用户确认 **全部按推荐关闭**。
- 已回写 [决策日志](decision-log.md)、[架构设计](../architecture-design.md)、[概要设计](../high-level-design.md) 与 README。
- 后续如需调整，按变更流程回写并记录原因；不再作为“开放问题”。
