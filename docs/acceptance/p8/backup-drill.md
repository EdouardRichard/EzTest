# P8 备份/恢复演练记录（Task 29）

- 验收时间：2026-10-29 15:17–15:18 +08:00
- 仓库/分支：`D:\Project_new\EzTest` / `dev`
- 演练时 HEAD：`1b8d6f988f5dd7f8a8542808fe6137f682943ceb`（`chore(p8): add security audit and headers`）
- 环境：Windows + Windows PowerShell 5.1.26100；便携 PostgreSQL 17（`.tools/pgsql/bin`，`localhost:5433`）；数据库 `eztest`（24 张 public 表、共 65 行，dump 87,475 字节）；内容目录使用确定性 fixture（见下）
- 结论：**PASS**。`scripts/test-backup.ps1` 89/89 断言通过；备份、恢复、逐文件 SHA-256、逐表行数、拒绝覆盖、篡改检测均符合预期。

## 1. 演练命令

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/test-backup.ps1
```

脚本默认在 `.tmp/p8-backup` 工作：

- 用 fixture 覆盖五个内容目录（evidence/reports/exports/version-attachments/objects），其中包含嵌套证据文件、空子目录、二进制对象文件，以及必须被排除的 `.env`/`.secrets` 诱饵；
- 用 `.env` 的 `EZTEST_DATABASE_URL` 备份真实 `eztest` 库；
- 新建临时库 `eztest_backup_drill_<timestamp>_test`，恢复并校验，最后清理临时库/目录。

## 2. RPO / RTO

| 项目 | 结论 |
| --- | --- |
| RPO | **按需备份，无 PITR**。没有 WAL 归档/流复制，最近一次成功备份之后的数据不可恢复；manifest 的 `rowCountsStable=true` 表示本次 dump 前后行数一致。 |
| RTO 目标 | **≤ 30 分钟**（预建空库/空目录 → 恢复 → 校验 → 应用启动） |
| 本机实测恢复 | 恢复命令整体墙钟 **2.22 s**（含 manifest/归档哈希校验、`pg_restore` 0.8 s、tar 解包、逐文件哈希、24 表行数比对、报告写入）；临时库建库本身 < 1 s |
| 本机实测备份 | 整体墙钟 **1.98 s**（manifest 内部计时 1.23 s，dump 0.3 s） |
| 整轮演练 | **10.44 s**（含 6 次拒绝/篡改用例、建库与清理；并行任务占用 CPU/IO 时耗时会上浮） |

> 这是 24 表 / 87 KB dump / 5 个 fixture 文件的本机口径，不代表生产规模；生产 RTO 需按真实数据量与存储重新演练。

## 3. 校验结果

### 3.1 备份 manifest 与载荷

- `manifest.json`：`formatVersion=1`；记录 `dev @ 1b8d6f9`、备份时间、耗时、6 个 artifact（1 个 DB dump + 5 个目录 tar）的相对路径/大小/SHA-256、5 个内容文件的大小/SHA-256、24 张表的行数、6 个配置键名（只有键名）、排除模式。
- 数据库 dump：87,475 字节，SHA-256 `c61297ae99f93433dbc4632ef1286136862889a9588d22f7a92093930bdb1f82`。
- 五个目录归档均存在且 SHA-256 与 manifest 一致；归档中**没有** `.env`、`.env.*`、`.secrets`、`secrets` 成员。
- 真实 `.env` 中的密码/密钥值未出现在任何备份文件中（测试逐字节扫描 4 个 secret 值）；备份树没有 `.env` 文件。

### 3.2 恢复与一致性

| 校验项 | 期望 | 结果 |
| --- | --- | --- |
| 数据库 dump/归档 SHA-256 | 与 manifest 一致 | 6/6 PASS |
| 恢复表数/行数 | 24 表逐表一致（共 65 行） | 24/24 PASS，0 mismatch |
| 恢复文件 | 5/5 存在、大小与 SHA-256 一致 | 5/5 PASS |
| 关键文件内容 | evidence sentinel 内容逐字节一致 | PASS |
| 空子目录 | 恢复后仍存在 | PASS |
| `.env`/`.secrets` 诱饵 | 不在备份、也不在恢复目录 | PASS |
| `restore-report.json` | `passed=true`、`files.verified=5`、`database.rowCountsMatch=true`、`secrets.included=false` | PASS |

### 3.3 安全拒绝与失败检测

| 用例 | 结果 |
| --- | --- |
| 目标库名不以 `_test`/`_e2e` 结尾且无 `-AllowAnyDatabase` | 非 0 退出，提示 allowlist 与 `-AllowAnyDatabase` |
| 目标目录非空 | 非 0 退出，提示必须为空 |
| 只给 `-Force`、缺少 `-ConfirmOverwrite` | 非 0 退出，提示需要第二次确认 |
| 目标是共享测试库 `eztest_test` | 非 0 退出（即使满足 `_test` 后缀也拒绝） |
| manifest 中 dump SHA-256 被篡改 | 非 0 退出，**在触碰目标前**报告 integrity 失败 |
| 恢复后篡改一个文件再 `-VerifyOnly` | 非 0 退出，报告 `passed=false` 并列出 SHA-256/大小不匹配 |

### 3.4 补充：真实默认目录布局

另用默认配置（不传目录覆盖）备份到 `.tmp/p8-backup/real`：本机此时 `EZTEST_EVIDENCE_DIR`/`EZTEST_REPORT_DIR`/`EZTEST_STORAGE_DIR` 对应目录尚不存在（记录为 `status=missing`），`exports`/`version-attachments` 为空目录（0 文件、归档 1,536 字节）。恢复该备份到临时库 `eztest_backup_real_test` 时，缺失目录按设计创建空目录并输出 warning，数据库 24 表行数一致，脚本 PASS；随后临时库已删除。

## 4. 密钥丢失说明

备份**不包含** `.env`、数据库密码、管理员密码、`llm_key` 或 `EZTEST_SECRET_KEYS`。

- `EZTEST_SECRET_KEYS` 是版本号 → base64 32 字节 AES-256 key 的 JSON；后端用它加密 `llm_configs` 的 LLM API Key 与自定义 headers（AES-256-GCM）。
- **如果 `EZTEST_SECRET_KEYS` 丢失且没有独立副本，数据库备份中的已加密 LLM 配置不可恢复**（不是“恢复失败”，而是密文永久无法解密），只能重新录入 LLM Key/Headers。
- 恢复后需运维另行提供：`EZTEST_DATABASE_URL`（含密码）、`EZTEST_SECRET_KEYS`、`EZTEST_SECRET_KEY_ACTIVE`；管理员密码仅用于全新空库首次创建管理员，恢复出的库通常已含管理员账号。

## 5. 已知限制

1. **无 PITR/WAL**：只能恢复到最近一次成功备份。
2. 备份是整库 + 整目录 tar：无增量、无去重、无内置加密；备份目录按业务数据同等权限保护。
3. 备份期间若有进程写入内容目录，归档可能是该文件某一时刻的字节；哈希以实际归档内容为准。建议低写入窗口备份。
4. `pg_dump` 是一致快照；`rowCountsStable=false` 时行数对账仅供参考，本次演练为 `true`。
5. 恢复不创建数据库/角色，需预建空库；不自动执行备份之后新增的迁移（应用启动时按既有迁移机制处理，跨大版本前需评估）。
6. 演练期间有并行任务在 `dev` 上提交/写入数据库（例如 `schema_migrations` 从 4 增至 11、audit 表出现写入）；每轮演练内部 `rowCountsStable` 均为 `true`，不同轮次行数会变化，属预期。
7. 本机 RTO 数据（1.77 s 恢复）不能直接外推到生产；生产必须用真实数据量、真实磁盘和真实密钥走一遍完整恢复。
8. 恢复端 `-Force` 只覆盖备份中存在的对象/文件，不删除目标中多余的文件（报告会列为 unexpected/warning）。

## 6. 证据（gitignored，本地）

| 路径 | 内容 |
| --- | --- |
| `.tmp/p8-backup/backup/manifest.json` | 本轮备份 manifest（含 commit、哈希、行数、配置键名） |
| `.tmp/p8-backup/restore-report.json` | 恢复校验报告（`passed=true`、24 表、5 文件） |
| `.tmp/p8-backup/test-report.json` | 演练报告（89/89 断言、耗时、manifest 摘要） |
| `.tmp/task29-drill-final.log` | 完整命令输出 |
| `.tmp/p8-backup/real/`、`.tmp/p8-backup/real-restore/` | 补充的真实默认目录布局备份/恢复结果 |
