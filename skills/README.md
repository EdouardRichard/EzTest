# skills/ — 技能包播种源（seed source）

本目录是 EzTest 技能库的**首启播种源**，不是运行态权威存储。

- 运行态权威 = PostgreSQL `skill` / `skill_version` 表（agent 上下文）。
- 首次启动时，后端把每个 `skills/<name>/SKILL.md` 播种入库（幂等）；
  **已存在的技能行（含管理员编辑与删除）永不被播种覆盖或复活**。
- 运行态修改/新增/删除技能走管理面（`/api/skills` 族，admin）或前端「技能」页，
  **不需要改代码、重新构建或重启进程**；每次修改追加一个 append-only 版本并写审计
  （`skill.created` / `skill.updated` / `skill.deleted`）。
- 技能正文只存在于本目录文件与数据库，**不得硬编码进 Go 源码**（有 grep 断言测试）。

## 包格式

```
skills/
  <name>/            # 包名：^[a-z][a-z0-9_-]{0,63}$
    SKILL.md         # frontmatter（name/description）+ markdown 正文
```

`SKILL.md` 示例：

```markdown
---
name: example-skill
description: 一句话说明这个技能解决什么问题（元数据层，列表/披露用）
---

# 正文（按需读取层）

具体的工作法、命令用法、注意事项……
```

- frontmatter 仅支持 `name` 与 `description` 两个键；`name` 必须与目录名一致。
- 正文非空，≤ 32768 字符；description ≤ 2000 字符。
- 包目录缺 `SKILL.md`、frontmatter 不合法都会让播种**失败封闭**（不静默跳过）。

内置技能包：`playwright-cli`（官方 CLI 浏览器自动化用法）与 `api-recon`（HTTP API 侦察与测试工作法）。

## 渐进披露（progressive disclosure）

- 每次 agent run 的上下文只常驻**元数据层**（name/description，pinned facts，压缩不丢）。
- 正文按需：模型调用 `skill.read(name)` 读取 SKILL.md 正文；每次读取（命中/未命中）追加一条
  `skill_usage` 事实（run_id/seq/skill_name/found，append-only，(run_id,seq) 幂等）。
- 管理面可经 `GET /api/skill-usage?run_id=`（admin）回读某次 run 的技能使用账本。