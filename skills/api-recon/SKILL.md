---
name: api-recon
description: HTTP API 侦察与测试工作法：从已有目录与真实流量发现端点，用 http.request 探测验证，结合数据库校验与证据记录完成 API 断言。
---

# API 侦察与测试工作法

本技能教你在 EzTest 里系统地验证 HTTP API 行为：先读已有知识，再观察真实流量，然后探测验证并记录断言。EzTest 是 E2E 测试平台——只验证用例要求的行为，不做渗透测试（不爆破、不利用漏洞、不越范围扫描）。

## 工作流

### 1. 先读已有 SUT 知识（不要盲目探测）

- `context.task_pack`：用例的 SUT 绑定里有 base_url、已知 API 端点与账号角色。
- `sut.list` / `sut.page_list` / `design.graph_read`：环境目录里已登记的页面与 API。
- 有已索引代码仓时，code 事实（路由/端点）是端点形状的可信来源。
- 目录命中就不必重新发现；目录缺失或过期才进入下一步。

### 2. 观察真实流量发现端点

- 用 `browser.cli` 走一遍相关 UI 流程，然后 `requests [--all] [--filter=<regex>]` 列出页面实际发出的请求；`request <index>`、`request-headers/body`、`response-headers/body <index>` 读明细。
- 真实流量给出的方法/路径/请求体形状比猜测可靠；把它作为探测的起点。
- 已录流量同时自动落为 network 证据，可经 `execution.evidence_read` 回读。

### 3. 用 http.request 探测验证

- 按用例断言构造请求：方法、URL、头、请求体。`http.request` 不限 host，响应超限自动 spill（用 `tool_result.read` 分页回读，不要假设截断后的内容完整）。
- 需要认证时：先经登录接口或 UI 登录拿 token/cookie（凭据来自 `account.credentials`，明文 by design），再带认证头探测；不要伪造认证态。
- 验证点来自断言 expected：状态码、响应结构、字段值、错误文案。逐项核对，不要只看 200。
- 负向验证按用例要求构造非法输入并核对预期错误响应；这是验收测试，不是模糊测试。
- 需要批量/管道处理（如从响应提取变量再发下一个请求）时用 `exec.run` + curl/jq；spill 产物在工作区可直接处理。

### 4. 交叉校验数据库状态

- 写操作类断言（创建/更新/删除）先 `database.schema_read` 确认表名列名，再 `db.query` 查实际落库状态（读写均可）。
- API 响应与 DB 状态不一致本身就是缺陷证据：两边都记录下来。
- 数据准备/清理用 `db.query` 写 SQL 建立 Given 前置；清理只动本用例造的数据。

### 5. 记录与收尾

- 每步 `execution.step_record`（seq/status/observed），每条断言 `execution.assertion_record`（verdict/actual/reason）；expected 以任务上下文为准，服务端覆写。
- HTTP/DB 证据由工具自动落库；关键观察补 `execution.evidence` 文本。
- 断言失败如实记录 failed，不虚构 passed；环境不可用/缺凭据记 blocked 并说明原因。
- 全部步骤与断言记录完成后立即 `execution.case_finish` 提交候选终态。

## 纪律

- 端点存在性先验证再断言：不要对猜测的 URL 直接下结论。
- 探测失败连续 2 次换路径（UI 流量观察 / DB 校验 / 人工接管），不要空转重试。
- 发现的新端点/页面属于执行经验：按用例断言记录即可，资产录入走服务端门禁，不在本技能范围内自行登记。