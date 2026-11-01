# EzTest 0.1 文档归档

> 状态：**0.1（预览版）归档**，仅作历史参考。
> 0.2 权威文档：[docs/version-V0.2/README.md](../version-V0.2/README.md)、[deploy/README.md](../../deploy/README.md)。
> 归档文档正文不重写；新增内容不得以 0.1 文档为依据。

## 归档内容

| 文档 | 内容 |
|---|---|
| [DEVELOPMENT_STATUS.md](DEVELOPMENT_STATUS.md) | 0.1 分支 P0–P8 的完整开发状态长历史（2026-10-30 快照，100 KB） |
| [architecture-design.md](../architecture-design.md) | 0.1 架构设计文档（16 个限界上下文、67 表、47 迁移、OpenAPI 223 路由） |
| [detailed-design.md](../detailed-design.md) | 0.1 详细设计文档（实现级） |
| [deployment.md](../deployment.md) | 0.1 部署指南（P8） |
| [operations.md](../operations.md) | 0.1 运维手册（备份/GC/多实例/限流/SLO） |
| [user-guide.md](../user-guide.md) | 0.1 用户手册（P8） |
| [development.md](../development.md) | 0.1 开发指南（P8） |
| [openapi/api.yaml](../openapi/api.yaml) | 0.1 OpenAPI 契约（`0.9.0`，223 operations） |
| [acceptance.md](../../acceptance.md) | 0.1 P7 真实浏览器人工验收记录 |
| [docs/acceptance/p7/](../acceptance/p7/) | 0.1 P7 验收截图（继续中、DB 目标、问题/报告、真实 LLM 走查） |
| [docs/acceptance/p8/](../acceptance/p8/) | 0.1 P8 验收（acceptance、SLO、安全审计、备份演练、E2E 截图） |
| [docs/release-notes/0.1-preview.md](../release-notes/0.1-preview.md) | 0.1-preview 发布说明 |
| [docs/release-notes/p8.md](../release-notes/p8.md) | 0.1 P8 发布说明 |
| [docs/design/system-blueprint.md](../design/system-blueprint.md) | 0.1 系统蓝图（历史设计权威） |

## 0.1 遗留资产说明

- 根目录 0.1 文档顶部均有“0.1 归档”横幅；`docs/openapi/api.yaml` 以 YAML 注释标注。
- `scripts/acceptance.ps1`、`scripts/slo.ps1`、`scripts/security-audit.ps1`、`scripts/build-release.ps1`、
  `scripts/acceptance-p8-llm.ps1` 等是 0.1 脚本，**不再是 0.2 的验收门禁**；0.2 验收入口见
  [docs/version-V0.2/acceptance/p8/runbook.md](../version-V0.2/acceptance/p8/runbook.md)。
- 0.1 与 0.2 数据库不兼容：0.2 使用新库，不做 0.1 原地迁移（见
  [docs/version-V0.2/README.md](../version-V0.2/README.md) 参数默认第 5 项）。
- 0.1 的端到端验收、SLO、安全审计等结论只适用于 0.1 基线，不得作为 0.2 的验证声明。
