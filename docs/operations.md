# EzTest 运维手册

> 本文件由 P8 任务按章节补充。当前章节：备份/恢复与密钥管理（Task 29）。
> GC、SLO、多实例单活、安全审计与故障处理章节由对应任务追加，勿在本文件重复维护同一主题。

## 备份与恢复

### 目标与口径

- **备份内容**：PostgreSQL 业务库（`pg_dump -Fc` 自定义格式）、证据/报告/导出/附件/对象五个内容目录（tar 归档）、`manifest.json`。
- **不备份内容**：`.env`、`.env.*`、`.secrets/`、`secrets/`，以及任何私钥/密码/`EZTEST_SECRET_KEYS`。备份目录只包含业务数据与清单，不包含用于解密业务数据的密钥。
- **RPO（恢复点目标）**：**按需备份，没有 PITR**。没有 WAL 归档/流复制，两次成功备份之间写入的数据不可恢复；生产环境应按业务可接受丢失窗口制定定时备份（例如每日一次 + 重要变更前一次）。详见 `docs/acceptance/p8/backup-drill.md` 的实测记录。
- **RTO（恢复时间目标）**：目标 **≤ 30 分钟**（新建空库/空目录 → 恢复 → 校验 → 应用启动）。实际耗时随数据库体积、磁盘与内容目录大小增长；本机演练数据见 `docs/acceptance/p8/backup-drill.md`。
- **一致性**：`pg_dump` 是数据库的一致快照；manifest 在 dump 前后各统计一次 public 表行数，两次不一致时记录 `rowCountsStable=false`，提示备份期间有并发写入（dump 本身仍可用，但行数校验仅作参考）。

### 执行备份

命令（在仓库根目录执行；PowerShell 5.1 或 7+）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/backup.ps1 `
  -TargetDir D:\eztest-backups\2026-10-29-1530
```

- `-TargetDir` 必须不存在或为空目录；脚本拒绝向非空目录写入，避免新旧备份混在一起。
- 数据库连接默认读取 `.env` 的 `EZTEST_DATABASE_URL`，密码只通过 `PGPASSWORD` 传给客户端工具，不打印、不写入 manifest。可用 `-DatabaseUrl` 显式指定其它源库。
- 内容目录默认读取（相对仓库根目录解析）：
  - `EZTEST_EVIDENCE_DIR`（默认 `.data/evidence`）
  - `EZTEST_REPORT_DIR`（默认 `.data/reports`）
  - `EZTEST_EXPORT_DIR`（默认 `.data/exports`）
  - `EZTEST_VERSION_ATTACHMENT_DIR`（默认 `.data/version-attachments`）
  - `EZTEST_STORAGE_DIR`（默认 `.data/objects`）
  也可用 `-EvidenceDir`、`-ReportDir`、`-ExportDir`、`-AttachmentDir`、`-ObjectDir` 临时覆盖。
- 目录不存在时不会失败，manifest 中记录 `status: "missing"`（恢复时创建空目录并给出 warning）。
- `.env` 不在仓库根之外的内容目录中，因此不会被 tar；若某个配置目录等于仓库根或其祖先，脚本直接拒绝（防止把 `.env` 打进归档）。

备份目录结构：

```text
<target>/
  manifest.json
  db/<database>.dump            # pg_dump -Fc
  directories/evidence.tar
  directories/reports.tar
  directories/exports.tar
  directories/version-attachments.tar
  directories/objects.tar
```

`manifest.json` 关键字段：

| 字段 | 含义 |
| --- | --- |
| `formatVersion` | 清单格式版本（当前 1） |
| `repository.commit` / `branch` / `version` | 备份时的 git commit、分支、`git describe` |
| `database.dumpFile` / `sizeBytes` / `sha256` | 数据库 dump 的相对路径、大小、SHA-256 |
| `database.rowCounts` / `rowCountsStable` | public 表逐表行数；dump 前后是否一致 |
| `directories[].configKey` / `archive` / `sha256` / `files[]` | 内容目录对应的配置键名、归档路径、归档哈希，以及逐文件相对路径/大小/SHA-256 |
| `configKeys` | 参与备份的配置键名（**只有键名，没有值**） |
| `excluded` | 明确排除的 `.env`/密钥路径模式 |
| `summary` | 目录数、文件数、字节数 |

脚本退出码：`0` 成功并写出 `manifest.json`；非 `0` 失败，失败时不得使用目标目录进行恢复（可能只有部分文件，且没有有效 manifest）。

### 执行恢复

前置条件：

1. 目标 PostgreSQL 实例已存在，且已用 `createdb` 新建**空数据库**。恢复脚本不会创建数据库或角色；也不要恢复进生产库。
2. 目标内容根目录不存在或为空。
3. 运维已单独提供 `.env` 与密钥（见下一节）；恢复脚本不会也**无法**从备份中还原密钥。
4. 目标库的 PostgreSQL 主版本与备份时一致或更新（`pg_dump` 自定义格式建议使用同版本/更新版本的 `pg_restore`）。

命令示例：

```powershell
# 1) 新建空库（密码通过 PGPASSWORD 或交互输入，不要写进命令行历史）
createdb -h localhost -p 5433 -U eztest eztest_restore_20261029_test

# 2) 恢复数据库 + 内容目录，并输出校验报告
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/restore.ps1 `
  -BackupDir D:\eztest-backups\2026-10-29-1530 `
  -TargetDatabase eztest_restore_20261029_test `
  -TargetRoot D:\eztest-restore\2026-10-29-1530
```

- 目标库名默认必须是 `*_test` 或 `*_e2e`；且拒绝 `postgres`/`template0`/`template1`、`.env` 中的源库名、`EZTEST_TEST_DATABASE_URL` 指向的共享测试库。确需恢复进其它库时必须显式 `-AllowAnyDatabase`（这是危险操作，仍会拒绝系统库与源库本身）。
- 非空目标库/目录会被拒绝。确需覆盖时必须同时给出 `-Force -ConfirmOverwrite`（第二次确认使用显式开关，避免在 CI/无人值守环境卡在 stdin）。`-Force` 恢复数据库时使用 `pg_restore --clean --if-exists`；内容目录采用覆盖写入，**不会删除**目标中不在备份里的多余文件（校验报告会列在 `files.unexpected`/warnings 中）。
- 恢复前会先校验 manifest 记录的所有归档大小与 SHA-256，并检查 tar 成员无绝对路径/`..`；任何一项不通过都不会改动目标。
- 恢复使用 `pg_restore --no-owner --no-privileges`：对象归执行恢复的数据库用户所有，不依赖原实例的角色/授权。
- 恢复完成后逐文件校验 SHA-256，并逐表比较行数，写出 `restore-report.json`（默认在 `-TargetRoot` 下）。任一文件缺失/哈希不符或行数不符时脚本以非 `0` 退出，报告 `passed: false`。

恢复后复核（不改动目标）：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/restore.ps1 `
  -BackupDir D:\eztest-backups\2026-10-29-1530 `
  -TargetDatabase eztest_restore_20261029_test `
  -TargetRoot D:\eztest-restore\2026-10-29-1530 `
  -ReportPath D:\eztest-restore\verify-report.json `
  -VerifyOnly
```

恢复判定为成功后，再切换应用连接串/启动服务。若校验失败，不要把目标库/目录当作可用恢复结果。

### 密钥管理与 `EZTEST_SECRET_KEYS` 丢失影响

- 备份**不包含** `.env`、数据库密码、管理员密码、`llm_key` 与 `EZTEST_SECRET_KEYS`。这些必须由运维使用独立的密码库/密钥管理系统保存，并与备份分开存放（备份泄露不应导致密钥泄露，密钥丢失也不应依赖备份找回）。
- `EZTEST_SECRET_KEYS` 是 JSON 对象：`{ "<版本号>": "<base64 编码的 32 字节 AES-256 key>" }`；`EZTEST_SECRET_KEY_ACTIVE` 指定当前加密使用的版本。后端用该 keyring 加密 `llm_configs.api_key` 与自定义 headers（AES-256-GCM，字段绑定 AAD）。
- **`EZTEST_SECRET_KEYS` 丢失且没有其它副本时，已加密的 LLM Key/Headers 不可恢复**。数据库备份里只有密文，没有密钥；只能重新录入 LLM 配置。同理，轮换时旧版本 key 必须保留到所有密文重新加密完成，否则旧密文不可解密。
- 恢复后至少需要运维另行提供：`EZTEST_DATABASE_URL`（含密码）、`EZTEST_SECRET_KEYS`、`EZTEST_SECRET_KEY_ACTIVE`，以及按需的 `EZTEST_ADMIN_*`（仅用于全新空库首次创建管理员；恢复出的库通常已有管理员，`EnsureAdmin` 不会覆盖）、`llm_*` 种子与执行器 token 等其它环境配置。
- 任何脚本与文档都不得打印密码/密钥；备份目录含业务数据，应按生产数据同等权限保护（例如 ACL/加密卷/对象存储服务端加密），本脚本不提供备份加密。

### 已知限制

- **无 PITR/WAL 归档**：只能恢复到最近一次成功备份的时间点。
- 备份为整库 + 整目录：无增量、无去重、无内置加密/压缩策略（tar 仅打包）；大库需自行估算磁盘与传输时间。
- 内容目录是 tar 整包；如果备份期间有进程正在写入某个大文件，可能得到该文件的中间状态（哈希以实际读到的内容为准），建议在低写入窗口备份。
- 恢复不创建数据库/角色，也不自动执行备份之后新增的迁移；应用启动时会按既有迁移机制补迁移，跨大版本恢复前应先确认兼容性。
- `rowCountsStable=false` 时行数校验只作参考（dump 仍是一致快照）；应在低写入窗口重新备份以获得可精确对账的 manifest。
- 本机演练使用便携 PostgreSQL 17 与本地磁盘，RTO 实测不代表生产规模；生产需按实际数据量做恢复演练。
- 恢复脚本的相对路径参数按仓库根目录解析；`-BackupDir`/`-TargetRoot` 也可使用绝对路径。

## 相关脚本与证据

| 脚本/文件 | 用途 |
| --- | --- |
| `scripts/backup.ps1` | 生成数据库 dump、目录 tar、`manifest.json` |
| `scripts/restore.ps1` | 安全恢复 + 逐文件 SHA-256/行数校验 + `restore-report.json`；`-VerifyOnly` 复核 |
| `scripts/test-backup.ps1` | 本机端到端演练（`.tmp/p8-backup`），失败非零退出并清理临时库/目录 |
| `docs/acceptance/p8/backup-drill.md` | 演练记录（RPO/RTO、校验结果、密钥说明） |
